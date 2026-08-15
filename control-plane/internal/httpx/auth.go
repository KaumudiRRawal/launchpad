package httpx

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/KaumudiRRawal/launchpad/control-plane/internal/domain"
)

const ctxKeyAccount ctxKey = 100

// Authenticator resolves a plaintext API token to the account that owns it.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (domain.Account, error)
}

// AccountFrom returns the authenticated account. The second result is false on
// an unauthenticated request, which cannot happen inside a handler mounted
// behind RequireAuth.
func AccountFrom(ctx context.Context) (domain.Account, bool) {
	account, ok := ctx.Value(ctxKeyAccount).(domain.Account)
	return account, ok
}

// MustAccountFrom returns the authenticated account and panics if there is
// none. Handlers behind RequireAuth use this: a missing account there is a
// routing mistake, and the recovery middleware turns the panic into a 500
// rather than letting the request proceed with no owner.
func MustAccountFrom(ctx context.Context) domain.Account {
	account, ok := AccountFrom(ctx)
	if !ok {
		panic("httpx: handler requires authentication but no account is present in the request context")
	}
	return account
}

// RequireAuth rejects any request without a valid `Authorization: Bearer`
// token and attaches the resolved account to the request context.
func RequireAuth(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				// RFC 7235: a 401 must say how to authenticate.
				w.Header().Set("WWW-Authenticate", `Bearer realm="launchpad"`)
				Error(w, r, http.StatusUnauthorized, "unauthenticated",
					"a bearer token is required")
				return
			}

			account, err := auth.Authenticate(r.Context(), token)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					// Unknown and revoked tokens are reported identically, so
					// a caller cannot probe which of their old keys still exist.
					w.Header().Set("WWW-Authenticate", `Bearer realm="launchpad", error="invalid_token"`)
					Error(w, r, http.StatusUnauthorized, "invalid_token",
						"the bearer token is not valid")
					return
				}
				LoggerFrom(r.Context()).Error("authenticate request", "error", err.Error())
				Error(w, r, http.StatusInternalServerError, "internal_error",
					"could not verify credentials")
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyAccount, account)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken extracts the credential from an Authorization header. The scheme
// is matched case-insensitively because RFC 7235 defines it that way and real
// clients send "bearer".
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}

	scheme, credential, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}

	credential = strings.TrimSpace(credential)
	if credential == "" {
		return "", false
	}
	return credential, true
}
