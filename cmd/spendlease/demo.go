package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/premhiru/spendlease/internal/dashboard"
	"github.com/premhiru/spendlease/internal/gateway"
	"github.com/premhiru/spendlease/internal/money"
	"github.com/premhiru/spendlease/internal/providers"
	"github.com/premhiru/spendlease/internal/providers/openai"
	"github.com/premhiru/spendlease/internal/store"
	"github.com/premhiru/spendlease/internal/store/sqlite"
	"github.com/premhiru/spendlease/internal/vault"
)

const demoRequestBody = `{"model":"gpt-4o","messages":[{"role":"user","content":"Process the next queued task."}],"max_tokens":500}`

type demoAgent struct {
	name      string
	principal store.Principal
	token     string
	interval  time.Duration
}

type synchronizedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// runDemo launches a throwaway gateway, mock provider and simulated fleet.
func runDemo(args []string, stdout, stderr io.Writer) error {
	safeOut := &synchronizedWriter{w: stdout}
	safeErr := &synchronizedWriter{w: stderr}
	fs := newFlagSet("demo", safeErr)
	target := fs.String("target", "http://localhost:4000", "URL where the demo dashboard listens")
	duration := fs.Duration("duration", 30*time.Second, "how long to run; 0 runs until interrupted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *duration < 0 {
		return fmt.Errorf("%w: -duration must not be negative", errUsage)
	}

	u, err := url.Parse(*target)
	if err != nil || u.Scheme != "http" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("%w: -target must be an http URL with no path", errUsage)
	}
	listener, err := net.Listen("tcp", u.Host)
	if err != nil {
		return fmt.Errorf("listening at %s: %w", *target, err)
	}
	defer func() { _ = listener.Close() }()

	logger := slog.New(slog.NewTextHandler(safeErr, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"demo-response","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":500,"completion_tokens":500}}`)
	}))
	defer mock.Close()

	st, err := sqlite.Open(ctx, sqlite.InMemory, sqlite.Options{Logger: logger})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	masterKey, err := vault.GenerateMasterKey()
	if err != nil {
		return err
	}
	v, err := vault.New(masterKey, st)
	if err != nil {
		return err
	}
	if err := v.Put(ctx, openai.Name, "demo-vendor-key"); err != nil {
		return err
	}
	oa, err := openai.NewWithBaseURL(mock.URL)
	if err != nil {
		return err
	}
	registry, err := providers.NewRegistry(oa)
	if err != nil {
		return err
	}
	book, err := loadPriceBook("", logger)
	if err != nil {
		return err
	}

	revocations := gateway.NewRevocationSet()
	killSwitch := gateway.NewKillSwitch(st, revocations)
	dash, err := dashboard.New(dashboard.Options{
		Store: st, Logger: logger, Version: version, Models: countModels(book), Revoker: killSwitch,
	})
	if err != nil {
		return err
	}
	gw, err := gateway.New(gateway.Options{
		Principals: st, Leases: st, Revocations: revocations, Credentials: v,
		Registry: registry, Recorder: gateway.NewRecorder(st, book, money.MustParseUSD("1.00"), logger),
		Dashboard: dash, Logger: logger,
	})
	if err != nil {
		return err
	}

	agents, err := seedDemoFleet(ctx, st)
	if err != nil {
		return err
	}
	// Duration describes how long the user can watch the running demo. Setup
	// time does not count: migrations and fleet seeding can be slower under
	// race instrumentation or on a cold machine.
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	srv := &http.Server{Handler: gw.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	actualURL := *target
	if u.Port() == "0" {
		actualURL = "http://" + listener.Addr().String()
	} else if u.Hostname() == "0.0.0.0" || u.Hostname() == "::" {
		actualURL = "http://127.0.0.1:" + u.Port()
	}
	fmt.Fprintf(safeOut, "spendlease demo dashboard: %s\n", actualURL)
	fmt.Fprintln(safeOut, "Simulating three agents. retry-loop has a $0.04 cap and retries every 50ms.")
	fmt.Fprintln(safeOut, "The kill switch will fire automatically; press Ctrl+C to stop early.")

	client := &http.Client{Timeout: 2 * time.Second}
	var fleet sync.WaitGroup
	for _, agent := range agents {
		agent := agent
		fleet.Add(1)
		go func() {
			defer fleet.Done()
			driveDemoAgent(ctx, client, actualURL, agent, safeOut)
		}()
	}

	killDelay := 2 * time.Second
	if *duration > 0 && *duration/3 < killDelay {
		killDelay = *duration / 3
	}
	if killDelay < 10*time.Millisecond {
		killDelay = 10 * time.Millisecond
	}
	fleet.Add(1)
	go func() {
		defer fleet.Done()
		select {
		case <-ctx.Done():
			return
		case <-time.After(killDelay):
		}
		for _, agent := range agents {
			if agent.name != "retry-loop" {
				continue
			}
			n, revokeErr := killSwitch.RevokePrincipal(context.Background(), agent.principal.ID)
			if revokeErr != nil {
				fmt.Fprintf(safeErr, "demo kill switch: %v\n", revokeErr)
				return
			}
			fmt.Fprintf(safeOut, "KILL SWITCH: revoked %d lease(s) for retry-loop\n", n)
		}
	}()

	var serveErr error
	select {
	case serveErr = <-errCh:
		stop()
	case <-ctx.Done():
	}
	// Race instrumentation and a cold SQLite connection can make cancellation
	// cleanup take a few seconds on Windows. Normal exits remain immediate;
	// this is only the upper bound for in-flight handlers.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	fleet.Wait()
	if serveErr != nil {
		return serveErr
	}
	return shutdownErr
}

func seedDemoFleet(ctx context.Context, st store.Store) ([]demoAgent, error) {
	specs := []struct {
		name     string
		budget   string
		ceiling  string
		interval time.Duration
	}{
		{name: "research-agent", budget: "0.20", ceiling: "0.15", interval: 650 * time.Millisecond},
		{name: "support-agent", budget: "0.20", ceiling: "0.15", interval: 900 * time.Millisecond},
		{name: "retry-loop", budget: "0.04", ceiling: "0.04", interval: 50 * time.Millisecond},
	}
	now := time.Now().UTC()
	agents := make([]demoAgent, 0, len(specs))
	for _, spec := range specs {
		_, keyHash := store.NewPrincipalKey()
		principal := store.Principal{
			ID: store.NewPrincipalID(), Name: spec.name, KeyHash: keyHash,
			Mode: store.ModeEnforce, CreatedAt: now,
		}
		if err := st.CreatePrincipal(ctx, principal); err != nil {
			return nil, err
		}
		run := store.Run{
			ID: store.NewRunID(), PrincipalID: principal.ID, Budget: money.MustParseUSD(spec.budget),
			Status: store.RunActive, CreatedAt: now,
		}
		if err := st.CreateRun(ctx, run); err != nil {
			return nil, err
		}
		token, tokenHash := store.NewLeaseToken()
		lease := store.Lease{
			ID: store.NewLeaseID(), RunID: run.ID, TokenHash: tokenHash,
			Providers: []string{openai.Name}, Ceiling: money.MustParseUSD(spec.ceiling),
			ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		}
		if err := st.CreateLease(ctx, lease); err != nil {
			return nil, err
		}
		agents = append(agents, demoAgent{name: spec.name, principal: principal, token: token, interval: spec.interval})
	}
	return agents, nil
}

func driveDemoAgent(ctx context.Context, client *http.Client, baseURL string, agent demoAgent, stdout io.Writer) {
	ticker := time.NewTicker(agent.interval)
	defer ticker.Stop()
	lastStatus := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewBufferString(demoRequestBody))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+agent.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(stdout, "%s: request failed: %v\n", agent.name, err)
			}
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != lastStatus {
			fmt.Fprintf(stdout, "%s: gateway returned %d\n", agent.name, resp.StatusCode)
		}
		lastStatus = resp.StatusCode
		if resp.StatusCode == http.StatusUnauthorized {
			return
		}
	}
}
