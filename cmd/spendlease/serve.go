package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/premhiru/spendlease/internal/controlplane"
	"github.com/premhiru/spendlease/internal/dashboard"
	"github.com/premhiru/spendlease/internal/gateway"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/anthropic"
	"github.com/premhiru/spendlease/internal/providers/openai"
	"github.com/premhiru/spendlease/internal/vault"
)

// shutdownGrace bounds how long in-flight requests have to finish on
// shutdown. Streaming completions can legitimately run for a while, so this
// is generous rather than snappy.
const shutdownGrace = 30 * time.Second

const (
	defaultKimiURL     = "https://api.moonshot.ai"
	defaultDeepSeekURL = "https://api.deepseek.com"
	defaultXAIURL      = "https://api.x.ai"
	defaultGeminiURL   = "https://generativelanguage.googleapis.com"
	defaultZAIURL      = "https://api.z.ai"
)

// runServe starts the gateway and blocks until the process is signalled.
func runServe(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	addr := fs.String("addr", ":4000", "address to listen on")
	storePath := storeFlag(fs)
	openAIBase := fs.String("openai-url", openai.DefaultBaseURL, "OpenAI upstream base URL")
	anthropicBase := fs.String("anthropic-url", anthropic.DefaultBaseURL, "Anthropic upstream base URL")
	kimiBase := fs.String("kimi-url", defaultKimiURL, "Kimi upstream base URL")
	deepSeekBase := fs.String("deepseek-url", defaultDeepSeekURL, "DeepSeek upstream base URL")
	xaiBase := fs.String("xai-url", defaultXAIURL, "xAI upstream base URL")
	geminiBase := fs.String("gemini-url", defaultGeminiURL, "Gemini upstream base URL")
	zaiBase := fs.String("zai-url", defaultZAIURL, "Z.AI upstream base URL")
	pricingDir := fs.String("pricing", "", "directory of price book YAML (default: the copy embedded in this binary)")
	defaultBudget := fs.String("default-run-budget", "10.00",
		"budget on a principal's implicit run")
	reservationTTL := fs.Duration("reservation-ttl", gateway.DefaultReservationTTL,
		"maximum lifetime of an in-flight budget hold")
	sweepInterval := fs.Duration("reservation-sweep-interval", gateway.DefaultReservationSweepInterval,
		"how often abandoned budget holds are reclaimed")
	adminTokenFlag := fs.String("admin-token", "",
		"credential required to reach the dashboard from off-machine (default: $"+EnvAdminToken+")")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error")
	if err := fs.Parse(args); err != nil {
		return err
	}

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Everything that can be validated without side effects is validated
	// first: configuration errors must not leave a migrated database or a
	// generated key file behind on a startup that was never going to succeed.
	oa, err := openai.NewWithBaseURL(*openAIBase)
	if err != nil {
		return fmt.Errorf("invalid -openai-url: %w", err)
	}
	an, err := anthropic.NewWithBaseURL(*anthropicBase)
	if err != nil {
		return fmt.Errorf("invalid -anthropic-url: %w", err)
	}
	compatible := []struct {
		name string
		raw  string
	}{
		{name: "kimi", raw: *kimiBase},
		{name: "deepseek", raw: *deepSeekBase},
		{name: "xai", raw: *xaiBase},
		{name: "gemini", raw: *geminiBase},
		{name: "zai", raw: *zaiBase},
	}
	adapters := []providers.Provider{oa, an}
	for _, cfg := range compatible {
		p, err := openai.NewCompatible(cfg.name, cfg.raw)
		if err != nil {
			return fmt.Errorf("invalid -%s-url: %w", cfg.name, err)
		}
		adapters = append(adapters, p)
	}

	registry, err := providers.NewRegistry(adapters...)
	if err != nil {
		return err
	}

	book, err := loadPriceBook(*pricingDir, logger)
	if err != nil {
		return err
	}
	summarisePriceBook(stdout, book)
	if *reservationTTL <= 0 {
		return fmt.Errorf("%w: -reservation-ttl must be positive", errUsage)
	}
	if *sweepInterval <= 0 {
		return fmt.Errorf("%w: -reservation-sweep-interval must be positive", errUsage)
	}

	// Then the master key, which in production must be supplied rather than
	// generated. Resolving it before opening the store means a refused
	// production startup creates nothing at all.
	masterKey, keySource, err := resolveMasterKey(*storePath)
	if err != nil {
		return err
	}
	// The source is logged; the key never is.
	logger.Info("master key loaded", "source", keySource)

	// Only now does anything touch persistent storage.
	st, err := openDatastore(ctx, *storePath, logger)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("closing store", "error", err)
		}
	}()

	v, err := vault.New(masterKey, st)
	if err != nil {
		return err
	}

	budget, err := money.ParseUSD(*defaultBudget)
	if err != nil {
		return fmt.Errorf("%w: invalid -default-run-budget: %v", errUsage, err)
	}

	adminToken := resolveAdminToken(*adminTokenFlag)
	revocations := gateway.NewRevocationSet()
	killSwitch := gateway.NewKillSwitch(st, revocations)
	pricingMetadata := book.Metadata(time.Now())
	guard := dashboard.Guard{Token: adminToken}
	dash, err := dashboard.New(dashboard.Options{
		Store: st, Logger: logger, Version: version,
		PricingRevision: pricingMetadata.Revision, PricingEffective: pricingMetadata.LatestEffective,
		PricingLoadedAt: pricingMetadata.LoadedAt, PricingProviders: pricingMetadata.Providers,
		PricingModels: pricingMetadata.Models, Warning: dashboardWarning(*addr, adminToken),
		Guard: guard, Revoker: killSwitch,
	})
	if err != nil {
		return err
	}
	api, err := controlplane.New(controlplane.Options{Store: st, Revoker: killSwitch, Guard: guard, Logger: logger})
	if err != nil {
		return err
	}
	reportAdminAccess(stdout, *addr, adminToken)

	gw, err := gateway.New(gateway.Options{
		Principals:  st,
		Leases:      st,
		Revocations: revocations,
		Credentials: v,
		Registry:    registry,
		Recorder:    gateway.NewRecorder(st, book, budget, logger, *reservationTTL),
		Dashboard:   gateway.RouteGroup{dash, api},
		Logger:      logger,
	})
	if err != nil {
		return err
	}
	gateway.StartReservationSweeper(ctx, st, *sweepInterval, logger)

	configured, err := v.Providers(ctx)
	if err != nil {
		return err
	}
	warnIfUnconfigured(stdout, registry.Names(), configured)

	srv := &http.Server{
		Addr:    *addr,
		Handler: gw.Handler(),
		// No write timeout: a streaming completion can legitimately take
		// minutes, and a deadline here would sever it mid-stream.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", *addr, "store", redactStore(*storePath), "version", version)
		fmt.Fprintf(stdout, "spendlease %s listening on %s\n", version, *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down", "grace", shutdownGrace.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}

// dashboardWarning returns the banner shown above the table, or empty.
//
// Access from outside the machine is refused without an admin token, so the
// remaining risk is a token that is weak or widely shared. Saying so on the
// page reaches somebody who has not read the deployment documentation.
func dashboardWarning(addr, adminToken string) string {
	if boundToLoopback(addr) || adminToken == "" {
		return ""
	}
	return "This gateway is reachable from the network. The controls on this page can switch " +
		"enforcement off, and the admin token is all that stands in front of them. Treat it " +
		"like a password and put TLS in front of this port."
}

// boundToLoopback reports whether a listen address is local-only.
func boundToLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "localhost", "[::1]":
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// reportAdminAccess tells the operator, at startup, whether the dashboard is
// reachable and how.
//
// A gateway bound to every interface with no admin token serves the dashboard
// to nobody but localhost. That is the safe outcome, and it is also
// surprising, so it is said out loud rather than discovered as a 403.
func reportAdminAccess(w io.Writer, addr, adminToken string) {
	if boundToLoopback(addr) {
		return
	}
	if adminToken == "" {
		fmt.Fprintf(w,
			"\nThe dashboard is bound to %s but no admin token is set, so it is reachable "+
				"only from this machine.\nSet %s (or --admin-token) to open it to the network.\n\n",
			addr, EnvAdminToken)
		return
	}
	fmt.Fprintf(w, "\nDashboard reachable on %s. An admin token is required from off-machine.\n\n", addr)
}

// warnIfUnconfigured tells the operator, at startup rather than on the first
// failed request, which providers have no credential stored.
func warnIfUnconfigured(stdout io.Writer, known, configured []string) {
	have := make(map[string]bool, len(configured))
	for _, c := range configured {
		have[c] = true
	}

	var missing []string
	for _, name := range known {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}

	fmt.Fprintf(stdout, "\nNo API key stored for: %v\n", missing)
	fmt.Fprintf(stdout, "Requests to those providers will be refused until you add one:\n")
	for _, name := range missing {
		fmt.Fprintf(stdout, "  spendlease keys provider set %s --key <your %s api key>\n", name, name)
	}
	fmt.Fprintln(stdout)
}

// parseLogLevel converts the flag value to a slog level.
func parseLogLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%w: log level %q is not debug, info, warn or error", errUsage, s)
	}
}
