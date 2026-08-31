package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/auth"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type stubValidator struct {
	identity shopping.SessionIdentity
	err      error
}

func (v stubValidator) ValidateSession(context.Context) (shopping.SessionIdentity, error) {
	return v.identity, v.err
}

func waitForState(t *testing.T, service *auth.Service, want auth.State) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if service.Status().State == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for state %s, last was %s", want, service.Status().State)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestConnectDrivesAcceptWithoutAnExternalCaller(t *testing.T) {
	observedAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	service := auth.NewService(stubValidator{identity: shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}})
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)

	if status := authenticator.Connect(); status.ActionRequired {
		t.Fatalf("unexpected status after Connect: %#v", status)
	}
	waitForState(t, service, auth.StateActive)
}

func TestConnectDoesNotRequestActionWhileAutomaticValidationIsRunning(t *testing.T) {
	started := make(chan struct{})
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		close(started)
		<-ctx.Done()
		return shopping.SessionIdentity{}, ctx.Err()
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	status := authenticator.Connect()
	if status.ActionRequired {
		t.Fatalf("automatic validation requested premature human action: %#v", status)
	}
	<-started
}

func TestConnectDrivesAcceptOnceReconnectRetries(t *testing.T) {
	service := auth.NewService(stubValidator{err: errors.New("upstream unavailable")})
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)

	authenticator.Connect()
	waitForState(t, service, auth.StateFailed)

	authenticator.Connect()
	waitForState(t, service, auth.StateFailed)
	if status := service.Status(); status.State != auth.StateFailed {
		t.Fatalf("second connect did not retry validation: %#v", status)
	}
}

func TestDriveAcceptOnceDoesNotDoubleFireWhileInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	calls := make(chan struct{}, 4)
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		calls <- struct{}{}
		close(started)
		<-release
		return shopping.SessionIdentity{}, errors.New("stop")
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)

	authenticator.Connect()
	<-started
	authenticator.driveAcceptOnce()
	authenticator.driveAcceptOnce()
	close(release)
	waitForState(t, service, auth.StateFailed)

	if len(calls) != 1 {
		t.Fatalf("validator called %d times while one Accept was in flight, want 1", len(calls))
	}
}

type validatorFunc func(context.Context) (shopping.SessionIdentity, error)

func (f validatorFunc) ValidateSession(ctx context.Context) (shopping.SessionIdentity, error) {
	return f(ctx)
}
