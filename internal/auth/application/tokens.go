package application

import (
	"context"
	"time"
)

// Identity must come from a verifier that validates signature, issuer, audience,
// expiry, verified email and the school's email/hosted domain.
type Identity struct{ Sub, Email, FullName string }

type IdentityVerifier interface {
	Verify(context.Context, string) (Identity, error)
}

// TokenService performs local cryptographic operations, not network I/O.
// Implementations must generate unpredictable refresh tokens and store only hashes.
type TokenService interface {
	NewRefreshToken() (string, error)
	HashRefreshToken(string) string
	AccessToken(userID string, expiresAt time.Time) (string, error)
}

type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// AuthResult carries identity resolved by the authentication transaction, never
// supplied by the client. HTTP adapters can use it to load the current profile.
type AuthResult struct {
	Tokens
	User User
}
