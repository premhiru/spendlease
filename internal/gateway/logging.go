package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// logRequests emits one structured line per request.
//
// What is deliberately absent is as important as what is present: no request
// body, no response body, no headers, and no credential in any form. Request
// bodies are prompts and prompts are the user's data; headers carry the very
// key material SECURITY.md promises never appears in logs. The fields here are
// the ones an operator needs to answer "which agent, which vendor, how long,
// what happened".
func (g *Gateway) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// Attribution is filled in by the layers below as they resolve it.
		// The holder has to be installed here, on the outside, because each
		// inner layer replaces the request and the outer one never sees it.
		info := &requestInfo{}
		r = r.WithContext(context.WithValue(r.Context(), ctxInfo, info))

		next.ServeHTTP(rec, r)
		duration := time.Since(start)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", duration.Milliseconds(),
			"bytes", rec.written,
		}
		if info.principalID != "" {
			attrs = append(attrs, "principal", info.principalID)
		}
		if info.provider != "" {
			attrs = append(attrs, "provider", info.provider)
		}
		if info.model != "" {
			attrs = append(attrs, "model", info.model)
		}
		if info.runID != "" {
			attrs = append(attrs, "run", info.runID)
		}
		if info.mode != "" {
			attrs = append(attrs, "mode", info.mode)
		}
		// Flush count is not the signal: the proxy flushes twice even for a
		// short response when the upstream sends no Content-Length, which
		// made every error look like a stream. The content type is what
		// actually distinguishes a server-sent event stream.
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
			attrs = append(attrs, "streamed", true, "flushes", rec.flushes)
		}
		if g.observer != nil {
			g.observer.ObserveRequest(r.URL.Path, info.provider, rec.status, duration, rec.written)
		}

		switch {
		case rec.status >= 500:
			g.logger.Error("request", attrs...)
		case rec.status >= 400:
			g.logger.Warn("request", attrs...)
		default:
			g.logger.Info("request", attrs...)
		}
	})
}

// statusRecorder captures the response status and size without buffering the
// body, and notes whether the response was flushed incrementally.
//
// It must forward Flush, because the proxy relies on flushing to stream and a
// wrapper that swallowed it would silently reintroduce buffering — the exact
// bug this phase is meant to avoid.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	// flushes counts incremental deliveries. A response flushed once is just
	// a short response; a response flushed many times was streamed.
	flushes int
}

// WriteHeader records the status code.
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Write records how much was sent.
func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.written += int64(n)
	return n, err
}

// Flush forwards to the underlying writer so streaming keeps working.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		s.flushes++
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer, which is
// how the standard library finds Flush and the deadline setters through
// wrappers like this one.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
