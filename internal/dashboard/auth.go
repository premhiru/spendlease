package dashboard

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// Guard decides who may see the dashboard and change enforcement.
//
// The problem it solves is a genuine tension. The 60-second quickstart is
// `docker run` followed by opening a browser, and putting a password in front
// of that would destroy it. But the mode toggle disables enforcement, so on an
// exposed port an unauthenticated version means anyone who can reach it can
// switch spending limits off.
//
// The resolution is to treat those as different situations rather than
// choosing one:
//
//   - Loopback is trusted. Somebody on the machine already has the database
//     and the master key; a password would protect nothing.
//   - Anything else needs the admin token.
//   - If no token is configured, non-loopback access is refused outright
//     rather than served. Failing closed is the only safe default for a
//     control whose absence is invisible.
type Guard struct {
	// Token is the admin credential. Empty means no token was configured, in
	// which case non-loopback access is refused.
	Token string
}

// Protect wraps a handler with the guard.
func (g Guard) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}

		if g.Token == "" {
			http.Error(w,
				"The dashboard and admin API are not available over the network because no admin "+
					"token is configured.\n\n"+
					"Set SPENDLEASE_ADMIN_TOKEN (or --admin-token) and restart, or reach this gateway "+
					"over loopback.\n",
				http.StatusForbidden)
			return
		}

		if !g.authorized(r) {
			// The realm makes a browser prompt, so the dashboard is reachable
			// without a login page. Any username is accepted; the password is
			// the token.
			w.Header().Set("WWW-Authenticate", `Basic realm="spendlease", charset="UTF-8"`)
			http.Error(w, "an admin token is required\n", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// authorized reports whether the request carries the admin token.
//
// Both shapes are accepted: a bearer token for scripts and curl, and basic
// auth so a browser can prompt without the dashboard needing a login page.
func (g Guard) authorized(r *http.Request) bool {
	if _, password, ok := r.BasicAuth(); ok && tokenMatches(password, g.Token) {
		return true
	}
	if after, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found {
		return tokenMatches(strings.TrimSpace(after), g.Token)
	}
	return false
}

// tokenMatches compares in constant time, so a caller cannot learn a valid
// prefix by measuring how long a rejection took.
func tokenMatches(presented, want string) bool {
	if presented == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(want)) == 1
}

// isLoopback reports whether a request came from the local machine.
//
// A malformed or unparseable address is treated as remote. Guessing "local"
// when the answer is unclear would fail open, which for a control like this
// is the wrong direction.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
