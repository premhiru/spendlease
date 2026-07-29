package dashboard

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/premhiru/spendlease/internal/store"
)

// guarded builds a dashboard with the given admin token.
func guarded(t *testing.T, token string) http.Handler {
	t.Helper()

	d, err := New(Options{
		Store: &fakeStore{summaries: []store.PrincipalSummary{
			principal("prn_a", "agent", store.ModeObserve, "1.00", 1, 1, 0, 0),
		}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Guard:  Guard{Token: token},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mux := http.NewServeMux()
	d.Routes(mux)
	return mux
}

// request sends a request from a given address with optional credentials.
func request(t *testing.T, h http.Handler, method, path, from string, auth func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("mode=enforce")
	}
	req := httptest.NewRequest(method, path, body)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.RemoteAddr = from
	if auth != nil {
		auth(req)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestLoopbackNeedsNoCredential preserves the 60-second quickstart. Somebody
// on the machine already has the database and the master key; a password
// would protect nothing and would cost the thing that makes this installable.
func TestLoopbackNeedsNoCredential(t *testing.T) {
	t.Parallel()

	h := guarded(t, "")

	for _, from := range []string{"127.0.0.1:5000", "[::1]:5000", "127.0.0.53:5000"} {
		t.Run(from, func(t *testing.T) {
			t.Parallel()

			if rec := request(t, h, http.MethodGet, "/", from, nil); rec.Code != http.StatusOK {
				t.Errorf("GET / from %s = %d, want 200", from, rec.Code)
			}
		})
	}
}

// TestRemoteWithoutTokenIsRefused is the fail-closed default. A control whose
// absence is invisible is worse than no control at all, so a gateway with no
// admin token configured serves the dashboard to nobody but localhost.
func TestRemoteWithoutTokenIsRefused(t *testing.T) {
	t.Parallel()

	h := guarded(t, "")

	tests := []struct {
		method, path string
	}{
		{http.MethodGet, "/"},
		{http.MethodGet, "/table"},
		{http.MethodPost, "/admin/principals/prn_a/mode"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			t.Parallel()

			rec := request(t, h, tt.method, tt.path, "203.0.113.9:5000", nil)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			// The refusal has to say how to fix it, or it reads as a bug.
			if !strings.Contains(rec.Body.String(), "SPENDLEASE_ADMIN_TOKEN") {
				t.Errorf("the refusal does not name the variable to set: %s", rec.Body.String())
			}
		})
	}
}

// TestRemoteWithTokenIsAllowed covers both credential shapes: a bearer token
// for scripts, and basic auth so a browser can prompt without a login page.
func TestRemoteWithTokenIsAllowed(t *testing.T) {
	t.Parallel()

	const token = "s3cret-admin-token"
	h := guarded(t, token)

	tests := []struct {
		name string
		auth func(*http.Request)
		want int
	}{
		{
			name: "bearer token",
			auth: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) },
			want: http.StatusOK,
		},
		{
			name: "basic auth, any username",
			auth: func(r *http.Request) { r.SetBasicAuth("admin", token) },
			want: http.StatusOK,
		},
		{
			name: "basic auth, empty username",
			auth: func(r *http.Request) { r.SetBasicAuth("", token) },
			want: http.StatusOK,
		},
		{
			name: "no credential",
			auth: nil,
			want: http.StatusUnauthorized,
		},
		{
			name: "wrong bearer token",
			auth: func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") },
			want: http.StatusUnauthorized,
		},
		{
			name: "wrong password",
			auth: func(r *http.Request) { r.SetBasicAuth("admin", "wrong") },
			want: http.StatusUnauthorized,
		},
		{
			name: "empty password",
			auth: func(r *http.Request) { r.SetBasicAuth("admin", "") },
			want: http.StatusUnauthorized,
		},
		{
			name: "a prefix of the token is not enough",
			auth: func(r *http.Request) { r.SetBasicAuth("admin", token[:len(token)-1]) },
			want: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := request(t, h, http.MethodGet, "/", "203.0.113.9:5000", tt.auth)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusUnauthorized {
				// A browser needs the challenge to prompt for a password.
				if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic ") {
					t.Errorf("WWW-Authenticate = %q, want a Basic challenge so a browser prompts", got)
				}
			}
		})
	}
}

// TestModeChangeIsGuarded is the specific route that matters: it disables
// enforcement.
func TestModeChangeIsGuarded(t *testing.T) {
	t.Parallel()

	const token = "tok"
	h := guarded(t, token)

	if rec := request(t, h, http.MethodPost, "/admin/principals/prn_a/mode", "203.0.113.9:5000", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("an unauthenticated remote mode change returned %d, want 401", rec.Code)
	}
	if rec := request(t, h, http.MethodPost, "/admin/principals/prn_a/mode", "203.0.113.9:5000",
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }); rec.Code != http.StatusOK {
		t.Errorf("an authenticated remote mode change returned %d, want 200", rec.Code)
	}
}

// TestStaticAssetsAreUnguarded: requiring credentials for the stylesheet would
// mean a browser could not render the page it is being asked to log in to.
func TestStaticAssetsAreUnguarded(t *testing.T) {
	t.Parallel()

	h := guarded(t, "")

	for _, asset := range []string{"/static/dashboard.css", "/static/htmx.min.js"} {
		t.Run(asset, func(t *testing.T) {
			t.Parallel()

			rec := request(t, h, http.MethodGet, asset, "203.0.113.9:5000", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if rec.Body.Len() == 0 {
				t.Error("the asset is empty")
			}
		})
	}
}

// TestAssetsAreServedFromTheBinary is the guard on the offline promise: a
// dashboard that goes blank without internet access is not self-hosted.
func TestAssetsAreServedFromTheBinary(t *testing.T) {
	t.Parallel()

	h := guarded(t, "")

	page := request(t, h, http.MethodGet, "/", "127.0.0.1:5000", nil).Body.String()
	for _, remote := range []string{"cdn.tailwindcss.com", "unpkg.com", "//cdn.", "https://cdn"} {
		if strings.Contains(page, remote) {
			t.Errorf("the page loads %q from the network; it must render offline", remote)
		}
	}
	for _, local := range []string{"/static/dashboard.css", "/static/htmx.min.js"} {
		if !strings.Contains(page, local) {
			t.Errorf("the page does not reference %s", local)
		}
	}

	// And htmx really is there, not a stub.
	js := request(t, h, http.MethodGet, "/static/htmx.min.js", "127.0.0.1:5000", nil).Body.String()
	if !strings.Contains(js, "htmx") || len(js) < 10_000 {
		t.Errorf("the vendored htmx looks wrong: %d bytes", len(js))
	}
}

func TestIsLoopback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:1234", true},
		{"[::1]:1234", true},
		{"127.0.0.53:1234", true},
		{"192.168.1.10:1234", false},
		{"203.0.113.9:1234", false},
		{"[2001:db8::1]:443", false},
		{"", false},
		{"garbage", false},
		{"not-an-ip:80", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			t.Parallel()

			if got := isLoopback(tt.addr); got != tt.want {
				t.Errorf("isLoopback(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}
