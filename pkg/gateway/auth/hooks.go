package auth

import (
	"context"
	"net/http"
	"time"
)

// AuthContext holds authentication information
type AuthContext struct {
	Subject     string
	Roles       []string
	Scopes      []string
	GeneratedAt time.Time
}

// Hook defines the interface for authentication/authorization hooks
type Hook interface {
	// OnConnect is called when a client attempts to connect
	OnConnect(ctx context.Context, r *http.Request) (*AuthContext, error)
	
	// OnMessage is called when a message is received (optional authorization)
	OnMessage(ctx context.Context, auth *AuthContext, msg []byte) error
}

// NoOpHook is a default implementation that allows everything
type NoOpHook struct{}

func (h *NoOpHook) OnConnect(ctx context.Context, r *http.Request) (*AuthContext, error) {
	return &AuthContext{
		Subject:     "anonymous",
		GeneratedAt: time.Now(),
	}, nil
}

func (h *NoOpHook) OnMessage(ctx context.Context, auth *AuthContext, msg []byte) error {
	return nil
}

// Chain allows multiple hooks to be chained
type Chain []Hook

func (c Chain) OnConnect(ctx context.Context, r *http.Request) (*AuthContext, error) {
	var lastAuth *AuthContext
	for _, h := range c {
		auth, err := h.OnConnect(ctx, r)
		if err != nil {
			return nil, err
		}
		if auth != nil {
			lastAuth = auth
		}
	}
	return lastAuth, nil
}

func (c Chain) OnMessage(ctx context.Context, auth *AuthContext, msg []byte) error {
	for _, h := range c {
		if err := h.OnMessage(ctx, auth, msg); err != nil {
			return err
		}
	}
	return nil
}
