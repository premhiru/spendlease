package dashboard

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/premhiru/spendlease/internal/operator"
	"github.com/premhiru/spendlease/internal/store"
)

// AdminRequestHeader is required on state-changing dashboard requests. A
// cross-origin HTML form cannot set it, which closes the localhost CSRF path.
const AdminRequestHeader = "X-Spendlease-Admin"

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
	// Operators resolves hashed, named operator credentials.
	Operators interface {
		GetOperatorByTokenHash(context.Context, string) (operator.Operator, error)
		HasActiveOperators(context.Context) (bool, error)
	}
	// Auditor receives two immutable records for an authenticated mutation:
	// an attempt before the handler and a result after it.
	Auditor interface {
		AppendOperatorAudit(context.Context, operator.AuditRecord) error
	}
	// OnAuditError reports a result-write failure. Attempt-write failures are
	// returned to the client before the mutation can run.
	OnAuditError func(error)
}

// Protect wraps a handler with the guard.
func (g Guard) Protect(next http.Handler) http.Handler {
	return g.ProtectRole(operator.RoleViewer, next)
}

// ProtectRole requires the named role. Higher roles inherit lower-role
// permissions: operators may read, and admins may operate and read.
func (g Guard) ProtectRole(required operator.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		local := isLoopback(r.RemoteAddr) && isLoopbackHost(r.Host)
		var identity operator.Identity
		if local {
			identity = operator.Identity{ID: "local", Name: "local", Role: operator.RoleAdmin}
		} else {
			if presentedToken(r) == "" {
				configured, err := g.remoteAccessConfigured(r.Context())
				if err != nil {
					http.Error(w, "operator authentication is temporarily unavailable\n", http.StatusServiceUnavailable)
					return
				}
				if !configured {
					http.Error(w,
						"The dashboard and admin API are not available over the network because no named operator is configured.\n\n"+
							"Create one with `spendlease keys operator create --name <name> --role admin`. "+
							"SPENDLEASE_ADMIN_TOKEN remains available only for migration from older releases.\n",
						http.StatusForbidden)
					return
				}
			}
			var ok bool
			var authErr error
			identity, ok, authErr = g.authorizedIdentity(r)
			if authErr != nil {
				http.Error(w, "operator authentication is temporarily unavailable\n", http.StatusServiceUnavailable)
				return
			}
			if !ok {
				// The realm makes a browser prompt, so the dashboard is reachable
				// without a login page. Named operators use their name as username.
				w.Header().Set("WWW-Authenticate", `Basic realm="spendlease", charset="UTF-8"`)
				http.Error(w, "a valid spendlease operator token is required\n", http.StatusUnauthorized)
				return
			}
		}

		if !identity.Role.Allows(required) {
			http.Error(w, "this operator role does not permit the requested action\n", http.StatusForbidden)
			return
		}
		if !safeMethod(r.Method) && !trustedMutation(r) {
			http.Error(w, "state-changing admin requests require the spendlease admin header and a same-origin browser request\n", http.StatusForbidden)
			return
		}

		r = r.WithContext(operator.WithIdentity(r.Context(), identity))
		if safeMethod(r.Method) || g.Auditor == nil {
			next.ServeHTTP(w, r)
			return
		}
		g.auditMutation(w, r, identity, next)
	})
}

func (g Guard) remoteAccessConfigured(ctx context.Context) (bool, error) {
	if g.Token != "" {
		return true, nil
	}
	if g.Operators == nil {
		return false, nil
	}
	return g.Operators.HasActiveOperators(ctx)
}

func (g Guard) auditMutation(w http.ResponseWriter, r *http.Request, identity operator.Identity, next http.Handler) {
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" || len(requestID) > 200 {
		requestID = operator.NewAuditID()
	}
	action := strings.TrimSpace(r.Pattern)
	if action == "" {
		action = r.Method + " " + r.URL.Path
	}
	base := operator.AuditRecord{
		RequestID: requestID, ActorID: identity.ID, ActorName: identity.Name, Role: identity.Role,
		Action: action, Resource: r.URL.RequestURI(), RemoteAddr: r.RemoteAddr,
	}
	attempt := base
	attempt.ID = operator.NewAuditID()
	attempt.Phase = "attempt"
	attempt.CreatedAt = time.Now().UTC()
	if err := g.Auditor.AppendOperatorAudit(r.Context(), attempt); err != nil {
		http.Error(w, "the audit trail is unavailable; the mutation was not attempted\n", http.StatusServiceUnavailable)
		return
	}
	capture := &statusCapture{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(capture, r)
	result := base
	result.ID = operator.NewAuditID()
	result.Phase = "result"
	result.StatusCode = capture.status
	result.CreatedAt = time.Now().UTC()
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := g.Auditor.AppendOperatorAudit(auditCtx, result); err != nil && g.OnAuditError != nil {
		g.OnAuditError(err)
	}
}

type statusCapture struct {
	http.ResponseWriter
	status int
}

func (w *statusCapture) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusCapture) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusCapture) Write(body []byte) (int, error) {
	return w.ResponseWriter.Write(body)
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func trustedMutation(r *http.Request) bool {
	if r.Header.Get(AdminRequestHeader) != "1" {
		return false
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// isLoopbackHost ensures that a reverse proxy or DNS-rebound hostname whose
// TCP peer happens to be local does not inherit credential-free admin access.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (g Guard) authorizedIdentity(r *http.Request) (operator.Identity, bool, error) {
	username, token, basic := r.BasicAuth()
	if !basic {
		token = presentedToken(r)
	}
	if token == "" {
		return operator.Identity{}, false, nil
	}
	if g.Operators != nil && operator.LooksLikeToken(token) {
		op, err := g.Operators.GetOperatorByTokenHash(r.Context(), operator.HashToken(token))
		if err == nil && op.Active() && (!basic || username == "" || username == op.Name) {
			return operator.Identity{ID: op.ID, Name: op.Name, Role: op.Role}, true, nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return operator.Identity{}, false, err
		}
	}
	if tokenMatches(token, g.Token) {
		return operator.Identity{ID: "legacy-admin", Name: "legacy-admin", Role: operator.RoleAdmin}, true, nil
	}
	return operator.Identity{}, false, nil
}

func presentedToken(r *http.Request) string {
	if _, token, ok := r.BasicAuth(); ok {
		return token
	}
	if after, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found {
		return strings.TrimSpace(after)
	}
	return ""
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
