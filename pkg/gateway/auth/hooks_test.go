package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type MockHook struct {
	OnConnectFunc func(context.Context, *http.Request) (*AuthContext, error)
	OnMessageFunc func(context.Context, *AuthContext, []byte) error
}

func (m *MockHook) OnConnect(ctx context.Context, r *http.Request) (*AuthContext, error) {
	if m.OnConnectFunc != nil {
		return m.OnConnectFunc(ctx, r)
	}
	return nil, nil
}

func (m *MockHook) OnMessage(ctx context.Context, auth *AuthContext, msg []byte) error {
	if m.OnMessageFunc != nil {
		return m.OnMessageFunc(ctx, auth, msg)
	}
	return nil
}

func TestChain_OnConnect(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		h1 := &MockHook{
			OnConnectFunc: func(ctx context.Context, r *http.Request) (*AuthContext, error) {
				return &AuthContext{Subject: "user1"}, nil
			},
		}
		h2 := &MockHook{}

		chain := Chain{h1, h2}
		auth, err := chain.OnConnect(context.Background(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth.Subject != "user1" {
			t.Errorf("expected user1, got %s", auth.Subject)
		}
	})

	t.Run("Failure", func(t *testing.T) {
		h1 := &MockHook{
			OnConnectFunc: func(ctx context.Context, r *http.Request) (*AuthContext, error) {
				return nil, errors.New("auth failed")
			},
		}
		chain := Chain{h1}
		_, err := chain.OnConnect(context.Background(), nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestChain_OnMessage(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		h1 := &MockHook{}
		h2 := &MockHook{}
		chain := Chain{h1, h2}
		if err := chain.OnMessage(context.Background(), nil, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("Failure", func(t *testing.T) {
		h1 := &MockHook{
			OnMessageFunc: func(ctx context.Context, a *AuthContext, b []byte) error {
				return errors.New("blocked")
			},
		}
		chain := Chain{h1}
		if err := chain.OnMessage(context.Background(), nil, nil); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
