package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/premhiru/spendlease/internal/controlplane"
	"github.com/premhiru/spendlease/internal/dashboard"
	"github.com/premhiru/spendlease/internal/gateway"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/observability"
	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/anthropic"
	"github.com/premhiru/spendlease/internal/providers/openai"
	"github.com/premhiru/spendlease/internal/vault"
)

// shutdownGrace bounds how long in-flight requests have to finish on
// shutdown. Streaming completions can legitimately run for a while, so this
// is generous rather than snappy.
const shutdownGrace = 30 * time.Second

const alertDrainGrace = 10 * time.Second

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
	alertWebhook := strings.TrimSpace(os.Getenv(EnvAlertWebhook))
	fs.StringVar(&alertWebhook, "alert-webhook", alertWebhook, "HTTPS endpoint for operational alerts")
	fs.Lookup("alert-webhook").DefValue = "$" + EnvAlertWebhook
	maxInFlight := fs.Int("max-inflight", 256, "maximum concurrent proxied requests (0 disables the limit)")
	requestReadTimeout := fs.Duration("request-read-timeout", 30*time.Second, "maximum time to read request headers and body")
	upstreamConnectTimeout := fs.Duration("upstream-connect-timeout", 10*time.Second, "vendor connection timeout")
	upstreamHeaderTimeout := fs.Duration("upstream-header-timeout", 5*time.Minute, "maximum wait for vendor response headers")
	upstreamTimeout := fs.Duration("upstream-timeout", 10*time.Minute, "total timeout for non-streaming vendor requests")
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
	if *maxInFlight < 0 {
		return fmt.Errorf("%w: -max-inflight cannot be negative", errUsage)
	}
	for name, value := range map[string]time.Duration{
		"request-read-timeout": *requestReadTimeout, "upstream-connect-timeout": *upstreamConnectTimeout,
		"upstream-header-timeout": *upstreamHeaderTimeout, "upstream-timeout": *upstreamTimeout,
	} {
		if value <= 0 {
			return fmt.Errorf("%w: -%s must be positive", errUsage, name)
		}
	}
	alertSecret := strings.TrimSpace(os.Getenv(EnvAlertWebhookSecret))
	if err := validateAlertWebhook(alertWebhook, alertSecret, strings.EqualFold(os.Getenv(EnvEnv), "production")); err != nil {
		return err
	}

	// Then the master key, which in production must be supplied rather than
	// generated. Resolving it before opening the store means a refused
	// production startup creates nothing at all.
	masterKeys, keySource, err := resolveMasterKeys(ctx, *storePath)
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

	v, err := vault.NewKeyring(masterKeys.Primary, masterKeys.Previous, st)
	if err != nil {
		return err
	}
	verifiedCredentials, err := v.Verify(ctx)
	if err != nil {
		return fmt.Errorf("verifying credential vault at startup: %w", err)
	}
	logger.Info("credential vault verified", "credentials", verifiedCredentials)

	budget, err := money.ParseUSD(*defaultBudget)
	if err != nil {
		return fmt.Errorf("%w: invalid -default-run-budget: %v", errUsage, err)
	}

	adminToken := resolveAdminToken(*adminTokenFlag)
	operators, err := st.ListOperators(ctx)
	if err != nil {
		return err
	}
	activeOperators := 0
	for _, op := range operators {
		if op.Active() {
			activeOperators++
		}
	}
	if adminToken != "" {
		logger.Warn("legacy admin token is enabled; migrate to named operator tokens", "environment", EnvAdminToken)
	}
	revocations := gateway.NewRevocationSet()
	killSwitch := gateway.NewKillSwitch(st, revocations)
	pricingMetadata := book.Metadata(time.Now())
	telemetry, err := observability.New(observability.Options{
		Store: st, Logger: logger, Version: version,
		WebhookURL: alertWebhook, WebhookSecret: alertSecret,
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), alertDrainGrace)
		defer cancel()
		if err := telemetry.Close(closeCtx); err != nil {
			logger.Error("draining alert webhook queue", "error", err)
		}
	}()
	guard := dashboard.Guard{
		Token: adminToken, Operators: st, Auditor: st,
		OnAuditError: func(err error) {
			logger.Error("recording operator audit result", "error", err)
			telemetry.Notify("audit_result_failed", nil)
		},
	}
	dash, err := dashboard.New(dashboard.Options{
		Store: st, Logger: logger, Version: version,
		PricingRevision: pricingMetadata.Revision, PricingEffective: pricingMetadata.LatestEffective,
		PricingLoadedAt: pricingMetadata.LoadedAt, PricingProviders: pricingMetadata.Providers,
		PricingModels: pricingMetadata.Models, Warning: operatorDashboardWarning(*addr, activeOperators, adminToken),
		Guard: guard, Revoker: killSwitch, Manager: st, LeaseRevoker: killSwitch,
		Credentials: v, CredentialStatus: v, Providers: registry.Names(),
	})
	if err != nil {
		return err
	}
	api, err := controlplane.New(controlplane.Options{Store: st, Audit: st, Revoker: killSwitch, Guard: guard, Logger: logger})
	if err != nil {
		return err
	}
	reportOperatorAccess(stdout, *addr, activeOperators, adminToken)

	upstreamTransport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: *upstreamConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 100, MaxIdleConnsPerHost: 20,
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: *upstreamConnectTimeout,
		ResponseHeaderTimeout: *upstreamHeaderTimeout, ExpectContinueTimeout: time.Second,
	}
	defer upstreamTransport.CloseIdleConnections()
	gw, err := gateway.New(gateway.Options{
		Principals:      st,
		Leases:          st,
		Revocations:     revocations,
		Credentials:     v,
		Registry:        registry,
		Recorder:        gateway.NewRecorder(st, book, budget, logger, *reservationTTL),
		Dashboard:       gateway.RouteGroup{dash, api, telemetry},
		Logger:          logger,
		Transport:       upstreamTransport,
		UpstreamTimeout: *upstreamTimeout,
		MaxInFlight:     *maxInFlight,
		Observer:        telemetry,
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
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       *requestReadTimeout,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
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

func validateAlertWebhook(rawURL, secret string, production bool) error {
	if rawURL == "" {
		if secret != "" {
			return fmt.Errorf("%s is set but %s is empty", EnvAlertWebhookSecret, EnvAlertWebhook)
		}
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("%w: alert webhook must be an http(s) URL without embedded credentials", errUsage)
	}
	if production && u.Scheme != "https" {
		return fmt.Errorf("%w: %s must use HTTPS in production", errUsage, EnvAlertWebhook)
	}
	if production && secret == "" {
		return fmt.Errorf("%w: %s is required when production alert delivery is enabled", errUsage, EnvAlertWebhookSecret)
	}
	return nil
}

// dashboardWarning returns the banner shown above the table, or empty.
//
// Access from outside the machine is refused without a named operator or the
// legacy token. The remaining transport and migration risks are stated on the
// page where an operator will see them.
func operatorDashboardWarning(addr string, activeOperators int, adminToken string) string {
	if boundToLoopback(addr) || (activeOperators == 0 && adminToken == "") {
		return ""
	}
	if adminToken == "" {
		return "This gateway is reachable from the network. Named operator roles protect its controls, " +
			"but they do not encrypt traffic. Put TLS at a trusted reverse proxy in front of this port."
	}
	return "This gateway is reachable from the network. The controls on this page can switch " +
		"enforcement off, and the legacy shared admin token is enabled. Migrate to named operators " +
		"and put TLS in front of this port."
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

// reportAdminAccess preserves the legacy helper used by focused tests.
func reportAdminAccess(w io.Writer, addr, adminToken string) {
	reportOperatorAccess(w, addr, 0, adminToken)
}

func reportOperatorAccess(w io.Writer, addr string, activeOperators int, adminToken string) {
	if boundToLoopback(addr) {
		return
	}
	if activeOperators == 0 && adminToken == "" {
		fmt.Fprintf(w,
			"\nThe dashboard is bound to %s but no named operator exists, so it is reachable "+
				"only from this machine.\nCreate one with `spendlease keys operator create --name <name> --role admin`.\n"+
				"For migration only, %s (or --admin-token) still enables the legacy shared credential.\n\n",
			addr, EnvAdminToken)
		return
	}
	if activeOperators > 0 {
		fmt.Fprintf(w, "\nDashboard reachable on %s. A named operator token is required from off-machine.\n\n", addr)
		return
	}
	fmt.Fprintf(w, "\nDashboard reachable on %s. A legacy admin token is required from off-machine.\n\n", addr)
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
