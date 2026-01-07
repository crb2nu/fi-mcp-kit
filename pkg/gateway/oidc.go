package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// OIDCAuthenticator validates JWT tokens against an OIDC provider.
type OIDCAuthenticator struct {
	Issuer   string
	ClientID string
	verifier *oidc.IDTokenVerifier
}

// NewOIDCAuthenticator creates a new OIDC authenticator.
// Call Initialize() before using Authenticate().
func NewOIDCAuthenticator(issuer, clientID string) *OIDCAuthenticator {
	return &OIDCAuthenticator{
		Issuer:   issuer,
		ClientID: clientID,
	}
}

// Initialize sets up the OIDC provider and verifier.
func (a *OIDCAuthenticator) Initialize(ctx context.Context) error {
	provider, err := oidc.NewProvider(ctx, a.Issuer)
	if err != nil {
		return fmt.Errorf("failed to create OIDC provider: %w", err)
	}

	a.verifier = provider.Verifier(&oidc.Config{
		ClientID: a.ClientID,
	})
	return nil
}

// Authenticate validates the token from the Authorization header.
func (a *OIDCAuthenticator) Authenticate(r *http.Request) error {
	if a.verifier == nil {
		return errors.New("OIDC authenticator not initialized")
	}

	token := r.Header.Get("Authorization")
	if len(token) < 8 || token[:7] != "Bearer " {
		token = r.URL.Query().Get("token")
		if token == "" {
			return errors.New("missing or invalid Authorization header")
		}
	} else {
		token = token[7:]
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	idToken, err := a.verifier.Verify(ctx, token)
	if err != nil {
		return fmt.Errorf("token verification failed: %w", err)
	}

	// Optionally extract claims for logging/auditing
	var claims struct {
		Email string `json:"email"`
		Sub   string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil {
		// Non-fatal: just log the issue
		return nil
	}

	return nil
}
