// Package observability provides readiness, Prometheus metrics, and bounded
// asynchronous webhook alerts without adding a monitoring-system dependency.
package observability

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	alertQueueSize = 128
	alertTimeout   = 5 * time.Second
	readyTimeout   = 2 * time.Second
)

// ReadyStore is the datastore liveness check used by /readyz.
type ReadyStore interface {
	PingContext(context.Context) error
}

// Options configures operational endpoints and optional webhook delivery.
type Options struct {
	Store         ReadyStore
	Logger        *slog.Logger
	Version       string
	WebhookURL    string
	WebhookSecret string
	HTTPClient    *http.Client
}

// Service owns process metrics and asynchronous alert delivery.
type Service struct {
	store   ReadyStore
	logger  *slog.Logger
	version string
	metrics metrics

	webhookURL    string
	webhookSecret []byte
	client        *http.Client
	queue         chan alert
	done          chan struct{}
	closeOnce     sync.Once
	queueMu       sync.RWMutex
	closed        bool
}

type metricKey struct {
	one   string
	two   string
	three string
}

type metrics struct {
	mu sync.RWMutex

	httpRequests map[metricKey]uint64
	httpDuration map[metricKey]float64
	httpBytes    map[metricKey]uint64
	budget       map[metricKey]uint64
	alerts       map[string]uint64
}

type alert struct {
	Version    string            `json:"version"`
	EventID    string            `json:"event_id"`
	Type       string            `json:"type"`
	OccurredAt time.Time         `json:"occurred_at"`
	Fields     map[string]string `json:"fields,omitempty"`
}

// New validates configuration and starts the webhook worker when configured.
func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("observability: Store is required")
	}
	if opts.Logger == nil {
		return nil, errors.New("observability: Logger is required")
	}
	service := &Service{
		store: opts.Store, logger: opts.Logger, version: opts.Version,
		metrics: metrics{
			httpRequests: map[metricKey]uint64{}, httpDuration: map[metricKey]float64{},
			httpBytes: map[metricKey]uint64{}, budget: map[metricKey]uint64{}, alerts: map[string]uint64{},
		},
	}
	if strings.TrimSpace(opts.WebhookURL) == "" {
		return service, nil
	}
	u, err := url.Parse(strings.TrimSpace(opts.WebhookURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("observability: webhook URL must be an http(s) URL without embedded credentials")
	}
	service.webhookURL = u.String()
	service.webhookSecret = []byte(opts.WebhookSecret)
	service.client = opts.HTTPClient
	if service.client == nil {
		service.client = &http.Client{Timeout: alertTimeout}
	} else {
		clientCopy := *service.client
		service.client = &clientCopy
	}
	service.client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("webhook redirects are refused")
	}
	service.queue = make(chan alert, alertQueueSize)
	service.done = make(chan struct{})
	go service.runAlerts()
	return service, nil
}

// Routes registers unauthenticated, metadata-only operational endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
}

func (s *Service) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	if err := s.store.PingContext(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"not_ready","reason":"datastore"}`+"\n")
		return
	}
	_, _ = io.WriteString(w, `{"status":"ready"}`+"\n")
}

// ObserveRequest records bounded-label HTTP totals, bytes, and duration.
func (s *Service) ObserveRequest(path, provider string, status int, duration time.Duration, written int64) {
	key := metricKey{one: surface(path), two: statusClass(status), three: safeProvider(provider)}
	s.metrics.mu.Lock()
	s.metrics.httpRequests[key]++
	s.metrics.httpDuration[key] += duration.Seconds()
	if written > 0 {
		s.metrics.httpBytes[key] += uint64(written)
	}
	s.metrics.mu.Unlock()
}

// ObserveBudget records one allowed, blocked, or would-block decision.
func (s *Service) ObserveBudget(provider, mode, outcome string) {
	key := metricKey{one: safeProvider(provider), two: safeMode(mode), three: safeOutcome(outcome)}
	s.metrics.mu.Lock()
	s.metrics.budget[key]++
	s.metrics.mu.Unlock()
}

// Notify queues one sanitized operational alert without blocking a request.
func (s *Service) Notify(kind string, fields map[string]string) {
	if s.queue == nil {
		return
	}
	s.queueMu.RLock()
	defer s.queueMu.RUnlock()
	if s.closed {
		return
	}
	event := alert{
		Version: "v1", EventID: newEventID(), Type: sanitizeLabel(kind),
		OccurredAt: time.Now().UTC(), Fields: cloneFields(fields),
	}
	select {
	case s.queue <- event:
	default:
		s.incrementAlert("dropped")
		s.logger.Error("alert queue is full; dropping event", "type", event.Type)
	}
}

func newEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("observability: cannot read random event ID: %v", err))
	}
	encoding := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
	return "alt_" + encoding.EncodeToString(b)
}

// Close drains queued alerts until ctx expires.
func (s *Service) Close(ctx context.Context) error {
	if s.queue == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.queueMu.Lock()
		s.closed = true
		close(s.queue)
		s.queueMu.Unlock()
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) runAlerts() {
	defer close(s.done)
	for event := range s.queue {
		if err := s.deliver(event); err != nil {
			s.incrementAlert("failed")
			s.logger.Error("delivering alert webhook", "type", event.Type, "event", event.EventID, "error", err)
			continue
		}
		s.incrementAlert("sent")
	}
}

func (s *Service) deliver(event alert) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), alertTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "spendlease/"+s.version)
			req.Header.Set("X-Spendlease-Event", event.EventID)
			if len(s.webhookSecret) > 0 {
				mac := hmac.New(sha256.New, s.webhookSecret)
				_, _ = mac.Write(body)
				req.Header.Set("X-Spendlease-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			}
			resp, requestErr := s.client.Do(req)
			if requestErr == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					cancel()
					return nil
				}
				requestErr = fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
				if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
					resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
					cancel()
					return requestErr
				}
			}
			lastErr = requestErr
		} else {
			lastErr = err
		}
		cancel()
	}
	return lastErr
}

func (s *Service) incrementAlert(outcome string) {
	s.metrics.mu.Lock()
	s.metrics.alerts[outcome]++
	s.metrics.mu.Unlock()
}

func (s *Service) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	s.metrics.mu.RLock()
	requests := cloneMetricMap(s.metrics.httpRequests)
	durations := cloneFloatMap(s.metrics.httpDuration)
	responseBytes := cloneMetricMap(s.metrics.httpBytes)
	budgets := cloneMetricMap(s.metrics.budget)
	alerts := make(map[string]uint64, len(s.metrics.alerts))
	for key, value := range s.metrics.alerts {
		alerts[key] = value
	}
	s.metrics.mu.RUnlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintln(w, "# HELP spendlease_build_info Build identity for the running gateway.")
	fmt.Fprintln(w, "# TYPE spendlease_build_info gauge")
	fmt.Fprintf(w, "spendlease_build_info{version=%s} 1\n", quoteLabel(s.version))
	writeMetricMap(w, "spendlease_http_requests_total", "counter", requests)
	writeFloatMetricMap(w, "spendlease_http_request_duration_seconds_sum", "counter", durations)
	writeMetricMap(w, "spendlease_http_response_bytes_total", "counter", responseBytes)
	writeBudgetMap(w, budgets)
	keys := sortedStringKeys(alerts)
	fmt.Fprintln(w, "# TYPE spendlease_alert_delivery_total counter")
	for _, outcome := range keys {
		fmt.Fprintf(w, "spendlease_alert_delivery_total{outcome=%s} %d\n", quoteLabel(outcome), alerts[outcome])
	}
}

func writeMetricMap(w io.Writer, name, metricType string, values map[metricKey]uint64) {
	fmt.Fprintf(w, "# TYPE %s %s\n", name, metricType)
	for _, key := range sortedMetricKeys(values) {
		fmt.Fprintf(w, "%s{surface=%s,status_class=%s,provider=%s} %d\n", name,
			quoteLabel(key.one), quoteLabel(key.two), quoteLabel(key.three), values[key])
	}
}

func writeFloatMetricMap(w io.Writer, name, metricType string, values map[metricKey]float64) {
	fmt.Fprintf(w, "# TYPE %s %s\n", name, metricType)
	for _, key := range sortedMetricKeys(values) {
		fmt.Fprintf(w, "%s{surface=%s,status_class=%s,provider=%s} %s\n", name,
			quoteLabel(key.one), quoteLabel(key.two), quoteLabel(key.three),
			strconv.FormatFloat(values[key], 'f', 6, 64))
	}
}

func writeBudgetMap(w io.Writer, values map[metricKey]uint64) {
	fmt.Fprintln(w, "# TYPE spendlease_budget_decisions_total counter")
	for _, key := range sortedMetricKeys(values) {
		fmt.Fprintf(w, "spendlease_budget_decisions_total{provider=%s,mode=%s,outcome=%s} %d\n",
			quoteLabel(key.one), quoteLabel(key.two), quoteLabel(key.three), values[key])
	}
}

func cloneMetricMap(source map[metricKey]uint64) map[metricKey]uint64 {
	out := make(map[metricKey]uint64, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func cloneFloatMap(source map[metricKey]float64) map[metricKey]float64 {
	out := make(map[metricKey]float64, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func sortedMetricKeys[T uint64 | float64](values map[metricKey]T) []metricKey {
	keys := make([]metricKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].one+"\x00"+keys[i].two+"\x00"+keys[i].three < keys[j].one+"\x00"+keys[j].two+"\x00"+keys[j].three
	})
	return keys
}

func sortedStringKeys(values map[string]uint64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func surface(path string) string {
	switch {
	case path == "/healthz":
		return "liveness"
	case path == "/readyz":
		return "readiness"
	case path == "/metrics":
		return "metrics"
	case strings.HasPrefix(path, "/api/"):
		return "operator_api"
	case strings.HasPrefix(path, "/admin/") || path == "/" || path == "/table":
		return "dashboard"
	default:
		return "proxy"
	}
}

func statusClass(status int) string { return strconv.Itoa(status/100) + "xx" }

func safeProvider(provider string) string {
	if provider == "" {
		return "none"
	}
	return sanitizeLabel(provider)
}

func safeMode(mode string) string {
	if mode == "observe" || mode == "enforce" {
		return mode
	}
	return "unknown"
}

func safeOutcome(outcome string) string {
	switch outcome {
	case "allowed", "blocked", "would_block", "limited":
		return outcome
	default:
		return "other"
	}
}

func sanitizeLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			out.WriteRune(r)
		}
	}
	if out.Len() == 0 {
		return "unknown"
	}
	return out.String()
}

func quoteLabel(value string) string {
	return strconv.Quote(value)
}

func cloneFields(fields map[string]string) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	for key, value := range fields {
		if len(value) > 500 {
			value = value[:500]
		}
		out[sanitizeLabel(key)] = value
	}
	return out
}
