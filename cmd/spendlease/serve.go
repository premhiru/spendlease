package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/premhiru/spendlease/internal/gateway"
	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/anthropic"
	"github.com/premhiru/spendlease/internal/providers/openai"
	"github.com/premhiru/spendlease/internal/store/sqlite"
	"github.com/premhiru/spendlease/internal/vault"
)

// shutdownGrace bounds how long in-flight requests have to finish on
// shutdown. Streaming completions can legitimately run for a while, so this
// is generous rather than snappy.
const shutdownGrace = 30 * time.Second

// runServe starts the gateway and blocks until the process is signalled.
func runServe(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("serve", stderr)
	addr := fs.String("addr", ":4000", "address to listen on")
	storePath := fs.String("store", "./spendlease.db", "SQLite file path")
	openAIBase := fs.String("openai-url", openai.DefaultBaseURL, "OpenAI upstream base URL")
	anthropicBase := fs.String("anthropic-url", anthropic.DefaultBaseURL, "Anthropic upstream base URL")
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

	st, err := sqlite.Open(ctx, *storePath, sqlite.Options{Logger: logger})
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("closing store", "error", err)
		}
	}()

	masterKey, keySource, err := resolveMasterKey(*storePath)
	if err != nil {
		return err
	}
	// The source is logged; the key never is.
	logger.Info("master key loaded", "source", keySource)

	v, err := vault.New(masterKey, st)
	if err != nil {
		return err
	}

	oa, err := openai.NewWithBaseURL(*openAIBase)
	if err != nil {
		return fmt.Errorf("invalid -openai-url: %w", err)
	}
	an, err := anthropic.NewWithBaseURL(*anthropicBase)
	if err != nil {
		return fmt.Errorf("invalid -anthropic-url: %w", err)
	}

	registry, err := providers.NewRegistry(oa, an)
	if err != nil {
		return err
	}

	gw, err := gateway.New(gateway.Options{
		Principals:  st,
		Credentials: v,
		Registry:    registry,
		Logger:      logger,
	})
	if err != nil {
		return err
	}

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
