package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
)

// The pre-shared token is an operator's replacement for signing in: one secret
// in the server's environment, the same secret in a client's config, and the
// client acts as the account the email variable names.
//
// It is accepted wherever a personal access token is, and only there.
// middleware.Auth, middleware.DaemonAuth and the realtime /ws authenticator
// consult ResolveSharedToken before they branch on a token's prefix. Signup,
// the human-versus-actor rules and the origin checks do not know it exists.
// Unset, every function below is a no-op and behaviour is unchanged.
const (
	SharedTokenEnv      = "MULTICA_AUTH_TOKEN"
	SharedTokenEmailEnv = "MULTICA_AUTH_TOKEN_EMAIL"
)

// SharedToken is the pre-shared credential. Everything but the user binding is
// fixed at construction; startup binds the user once the account exists.
type SharedToken struct {
	email string
	// SHA-256 of the secret. Both sides of a comparison are hashed, so it is
	// constant time for presented values of any length.
	digest [sha256.Size]byte
	// User ID (string). Empty until BindUser: an unbound token is nobody's.
	boundUser atomic.Value
}

// NewSharedToken keeps the secret exactly as given — no trimming, since a
// client sends what its own config holds — and normalizes the email the way
// the login handlers store it, so the lookup at startup finds the account.
func NewSharedToken(secret, email string) *SharedToken {
	return &SharedToken{
		email:  strings.ToLower(strings.TrimSpace(email)),
		digest: sha256.Sum256([]byte(secret)),
	}
}

// Email is the normalized account email the token belongs to.
func (t *SharedToken) Email() string { return t.email }

// BindUser makes the token authenticate as userID. Until this runs, the token
// is refused.
func (t *SharedToken) BindUser(userID string) { t.boundUser.Store(userID) }

// Match reports whether presented is the secret, in constant time.
func (t *SharedToken) Match(presented string) bool {
	if t == nil {
		return false
	}
	digest := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(digest[:], t.digest[:]) == 1
}

// Resolve maps a presented credential to the bound user. ok is false when
// presented is not the secret or no user is bound yet.
func (t *SharedToken) Resolve(presented string) (userID string, ok bool) {
	if !t.Match(presented) {
		return "", false
	}
	userID, _ = t.boundUser.Load().(string)
	if userID == "" {
		return "", false
	}
	return userID, true
}

var sharedToken atomic.Pointer[SharedToken]

// SetSharedToken installs the pre-shared credential for this process; nil (the
// initial state) disables it. main calls it once at startup.
func SetSharedToken(t *SharedToken) { sharedToken.Store(t) }

// ResolveSharedToken maps a presented credential to the account the pre-shared
// token belongs to. ok is false when none is configured, when presented is not
// the secret, and when startup has not bound it to a user yet.
func ResolveSharedToken(presented string) (userID string, ok bool) {
	return sharedToken.Load().Resolve(presented)
}

// SharedTokenFromEnv reads the two variables, once, at startup. It returns nil
// — the token stays refused — when the secret is unset, or when it is set
// without the email that says whose it is. The secret is never logged.
func SharedTokenFromEnv() *SharedToken {
	secret := os.Getenv(SharedTokenEnv)
	if secret == "" {
		return nil
	}
	email := strings.TrimSpace(os.Getenv(SharedTokenEmailEnv))
	if email == "" {
		slog.Error(SharedTokenEnv + " is set but " + SharedTokenEmailEnv +
			" is not; the token is refused. " + SharedTokenEmailEnv +
			" is required and must name the email of the account the token belongs to.")
		return nil
	}
	return NewSharedToken(secret, email)
}
