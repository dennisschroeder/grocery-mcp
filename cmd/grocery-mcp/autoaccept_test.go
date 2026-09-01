package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/auth"
	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
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

func TestLiveAuthenticatorStartsValidationWithoutAnAuthConnectCall(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	service := auth.NewService(stubValidator{identity: shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}})

	startAutoAcceptingAuthenticator(t.Context(), service)

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

func TestConnectRevalidatesAnActiveSession(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 2 {
			close(secondStarted)
			<-releaseSecond
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.Connect()
	waitForState(t, service, auth.StateActive)

	if status := authenticator.Connect(); status.State != shopping.AuthRefreshing {
		t.Fatalf("Connect() on Active = %#v, want Refreshing", status)
	}
	<-secondStarted
	close(releaseSecond)
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
}

func TestConnectQueuesRefreshWhileTheSuccessfulDriverIsExiting(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 9, 45, 0, 0, time.UTC)
	service := auth.NewService(stubValidator{identity: shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}})
	service.Connect()
	if err := service.Accept(t.Context(), browserbridge.NewTabBinding(observedAt)); err != nil {
		t.Fatal(err)
	}
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.running = true

	if status := authenticator.Connect(); status.State != shopping.AuthRefreshing {
		t.Fatalf("Connect() = %#v, want Refreshing", status)
	}
	authenticator.mu.Lock()
	restartPending := authenticator.restartPending
	authenticator.mu.Unlock()
	if !restartPending {
		t.Fatal("refresh was lost while the previous driver was exiting")
	}
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
	authenticator.mu.Lock()
	disconnectRevision := authenticator.disconnectRevision
	authenticator.mu.Unlock()
	authenticator.driveAcceptOnce(disconnectRevision)
	authenticator.driveAcceptOnce(disconnectRevision)
	close(release)
	waitForState(t, service, auth.StateFailed)

	if len(calls) != 1 {
		t.Fatalf("validator called %d times while one Accept was in flight, want 1", len(calls))
	}
}

func TestAutomaticValidationRetriesAfterAttemptTimeout(t *testing.T) {
	observedAt := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			<-ctx.Done()
			return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session"}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.retryInterval = time.Millisecond
	authenticator.validationTimeout = 10 * time.Millisecond

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
}

func TestUnresponsiveBridgeRetriesWithoutHumanAction(t *testing.T) {
	observedAt := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session"}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.retryInterval = time.Millisecond
	authenticator.actionDelay = 0

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
	if status := authenticator.Status(); status.ActionRequired {
		t.Fatalf("unresponsive bridge requested human action: %#v", status)
	}
}

func TestReauthRequiredRecoversAfterBrowserLoginWithoutAnotherConnectCall(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			return shopping.SessionIdentity{}, &shopping.AuthError{Operation: "validate session"}
		}
		close(secondStarted)
		select {
		case <-releaseSecond:
		case <-ctx.Done():
			return shopping.SessionIdentity{}, ctx.Err()
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.reauthRetryInterval = time.Millisecond

	authenticator.Connect()
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("browser login was never revalidated; status: %#v", authenticator.Status())
	}
	if status := authenticator.Status(); status.State != shopping.AuthReauthRequired || !status.ActionRequired {
		t.Fatalf("status during automatic revalidation = %#v", status)
	}
	close(releaseSecond)
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
}

func TestRefreshLogoutStartsAutomaticBrowserLoginRecovery(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 16, 30, 0, 0, time.UTC)
	thirdStarted := make(chan struct{})
	releaseThird := make(chan struct{})
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		switch attempts.Add(1) {
		case 1:
			return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
		case 2:
			return shopping.SessionIdentity{}, &shopping.AuthError{Operation: "validate session"}
		default:
			close(thirdStarted)
			select {
			case <-releaseThird:
			case <-ctx.Done():
				return shopping.SessionIdentity{}, ctx.Err()
			}
			return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt.Add(time.Minute)}, nil
		}
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.reauthRetryInterval = 50 * time.Millisecond

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	if err := authenticator.RefreshAndValidate(t.Context()); err == nil {
		t.Fatal("refresh unexpectedly succeeded after browser logout")
	}
	select {
	case <-thirdStarted:
		t.Fatal("browser login recovery retried before the reauth interval")
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case <-thirdStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("browser login recovery did not start; status: %#v", authenticator.Status())
	}
	if status := authenticator.Status(); status.State != shopping.AuthReauthRequired || !status.ActionRequired {
		t.Fatalf("status during browser login recovery = %#v", status)
	}
	close(releaseThird)
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 3 {
		t.Fatalf("validation attempts = %d, want 3", attempts.Load())
	}
}

func TestScheduledRefreshRecoveryCannotReconnectAfterDisconnect(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 16, 45, 0, 0, time.UTC)
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
		}
		return shopping.SessionIdentity{}, &shopping.AuthError{Operation: "validate session"}
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.reauthRetryInterval = 20 * time.Millisecond

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	if err := authenticator.RefreshAndValidate(t.Context()); err == nil {
		t.Fatal("refresh unexpectedly succeeded after browser logout")
	}
	if status := authenticator.Disconnect(); status.State != auth.StateUnauthenticated {
		t.Fatalf("disconnect status = %#v", status)
	}
	time.Sleep(5 * authenticator.reauthRetryInterval)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts after disconnect = %d, want 2", attempts.Load())
	}
	if status := authenticator.Status(); status.State != auth.StateUnauthenticated || status.ActionRequired {
		t.Fatalf("status after scheduled recovery = %#v", status)
	}
}

func TestConnectDuringSynchronousRefreshDoesNotSuppressRecovery(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		switch attempts.Add(1) {
		case 1:
			return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
		case 2:
			close(refreshStarted)
			<-releaseRefresh
			return shopping.SessionIdentity{}, &shopping.AuthError{Operation: "validate session"}
		default:
			return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt.Add(time.Minute)}, nil
		}
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.reauthRetryInterval = time.Millisecond

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- authenticator.RefreshAndValidate(t.Context())
	}()
	<-refreshStarted
	authenticator.Connect()
	close(releaseRefresh)
	if err := <-refreshDone; err == nil {
		t.Fatal("refresh unexpectedly succeeded after browser logout")
	}
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 3 {
		t.Fatalf("validation attempts = %d, want 3", attempts.Load())
	}
}

func TestSuccessfulConcurrentRecoveryIsNotMaskedAsReauthRequired(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 17, 15, 0, 0, time.UTC)
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		switch attempts.Add(1) {
		case 1:
			return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
		case 2:
			close(refreshStarted)
			<-releaseRefresh
			return shopping.SessionIdentity{}, &shopping.AuthError{Operation: "validate session"}
		default:
			return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt.Add(time.Minute)}, nil
		}
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- authenticator.RefreshAndValidate(t.Context())
	}()
	<-refreshStarted
	authenticator.mu.Lock()
	close(releaseRefresh)
	waitForState(t, service, auth.StateReauthRequired)
	service.Connect()
	if err := service.Accept(t.Context(), browserbridge.NewTabBinding(observedAt)); err != nil {
		authenticator.mu.Unlock()
		t.Fatalf("concurrent recovery failed: %v", err)
	}
	authenticator.mu.Unlock()
	if err := <-refreshDone; err == nil {
		t.Fatal("refresh unexpectedly succeeded after browser logout")
	}
	if status := authenticator.Status(); status.State != auth.StateActive || status.ActionRequired {
		t.Fatalf("successful recovery was masked: %#v", status)
	}
}

func TestOldReauthTimerCannotStartRecoveryInANewerEpisode(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 17, 30, 0, 0, time.UTC)
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		attempt := attempts.Add(1)
		if attempt == 2 || attempt == 4 {
			return shopping.SessionIdentity{}, &shopping.AuthError{Operation: "validate session"}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt.Add(time.Duration(attempt) * time.Minute)}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.reauthRetryInterval = 200 * time.Millisecond

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	if err := authenticator.RefreshAndValidate(t.Context()); err == nil {
		t.Fatal("first refresh unexpectedly succeeded after browser logout")
	}
	time.Sleep(50 * time.Millisecond)
	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	deadline := time.After(2 * time.Second)
	for {
		authenticator.mu.Lock()
		running := authenticator.running
		authenticator.mu.Unlock()
		if !running {
			break
		}
		select {
		case <-deadline:
			t.Fatal("explicit recovery driver did not stop")
		case <-time.After(time.Millisecond):
		}
	}

	if err := authenticator.RefreshAndValidate(t.Context()); err == nil {
		t.Fatal("second refresh unexpectedly succeeded after browser logout")
	}
	time.Sleep(170 * time.Millisecond)
	if attempts.Load() != 4 {
		t.Fatalf("old reauth timer started attempt %d in the newer episode", attempts.Load())
	}
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 5 {
		t.Fatalf("validation attempts = %d, want 5", attempts.Load())
	}
}

func TestStaleRetryCannotReconnectAfterDisconnect(t *testing.T) {
	service := auth.NewService(nil)
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.mu.Lock()
	authenticator.generation = 7
	authenticator.mu.Unlock()
	service.Connect()

	authenticator.Disconnect()
	if authenticator.retryConnect(7) {
		t.Fatal("stale retry reconnected after disconnect")
	}
	if status := service.Status(); status.State != auth.StateUnauthenticated {
		t.Fatalf("status after stale retry = %#v", status)
	}
}

func TestMissingBridgeRequestsHumanAction(t *testing.T) {
	service := auth.NewService(stubValidator{err: &shopping.BridgeUnavailableError{
		Operation:      "validate session",
		ActionRequired: true,
	}})
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.actionDelay = 0

	authenticator.Connect()
	deadline := time.After(2 * time.Second)
	for !authenticator.Status().ActionRequired {
		select {
		case <-deadline:
			t.Fatalf("missing bridge did not request human action: %#v", authenticator.Status())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestMissingBridgeRecoversWithoutAnotherConnectCall(t *testing.T) {
	observedAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	var available atomic.Bool
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if !available.Load() {
			return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session", ActionRequired: true}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.retryInterval = time.Millisecond
	authenticator.actionDelay = 0

	authenticator.Connect()
	deadline := time.After(2 * time.Second)
	for !authenticator.Status().ActionRequired {
		select {
		case <-deadline:
			t.Fatalf("missing bridge did not request human action: %#v", authenticator.Status())
		case <-time.After(time.Millisecond):
		}
	}

	available.Store(true)
	waitForState(t, service, auth.StateActive)
}

func TestDisconnectWaitsForDriverBeforeReconnect(t *testing.T) {
	observedAt := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	started := make(chan struct{})
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(ctx context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return shopping.SessionIdentity{}, ctx.Err()
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: observedAt}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)

	authenticator.Connect()
	<-started
	if status := authenticator.Disconnect(); status.State != auth.StateUnauthenticated {
		t.Fatalf("disconnect status = %#v", status)
	}
	authenticator.mu.Lock()
	running := authenticator.running
	authenticator.mu.Unlock()
	if running {
		t.Fatal("disconnect returned before the validation driver stopped")
	}

	authenticator.Connect()
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
}

func TestConnectDuringDriverExitHandsOffToAnotherValidation(t *testing.T) {
	base := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	actionCheck := make(chan struct{})
	releaseActionCheck := make(chan struct{})
	var nowCalls atomic.Int32
	var attempts atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		if attempts.Add(1) == 1 {
			return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session", ActionRequired: true}
		}
		return shopping.SessionIdentity{AccountID: "account", ObservedAt: base}, nil
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.actionDelay = time.Second
	authenticator.now = func() time.Time {
		if nowCalls.Add(1) == 3 {
			close(actionCheck)
			<-releaseActionCheck
			return base.Add(time.Second)
		}
		return base
	}

	authenticator.Connect()
	<-actionCheck
	if status := authenticator.Connect(); status.State != shopping.AuthBootstrapping {
		t.Fatalf("reconnect status = %#v", status)
	}
	close(releaseActionCheck)
	waitForState(t, service, auth.StateActive)
	if attempts.Load() != 2 {
		t.Fatalf("validation attempts = %d, want 2", attempts.Load())
	}
}

func TestDisconnectCannotRestoreActionRequiredFromExitingDriver(t *testing.T) {
	base := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	actionCheck := make(chan struct{})
	releaseActionCheck := make(chan struct{})
	var nowCalls atomic.Int32
	service := auth.NewService(validatorFunc(func(context.Context) (shopping.SessionIdentity, error) {
		return shopping.SessionIdentity{}, &shopping.BridgeUnavailableError{Operation: "validate session", ActionRequired: true}
	}))
	authenticator := newAutoAcceptingAuthenticator(t.Context(), service)
	authenticator.actionDelay = time.Second
	authenticator.now = func() time.Time {
		if nowCalls.Add(1) == 3 {
			close(actionCheck)
			<-releaseActionCheck
			return base.Add(time.Second)
		}
		return base
	}

	authenticator.Connect()
	<-actionCheck
	disconnected := make(chan shopping.AuthStatus, 1)
	go func() { disconnected <- authenticator.Disconnect() }()
	waitForState(t, service, auth.StateUnauthenticated)
	close(releaseActionCheck)
	if status := <-disconnected; status.State != shopping.AuthUnauthenticated {
		t.Fatalf("disconnect status = %#v", status)
	}
	if status := authenticator.Status(); status.ActionRequired {
		t.Fatalf("disconnect left action required: %#v", status)
	}
}

type validatorFunc func(context.Context) (shopping.SessionIdentity, error)

func (f validatorFunc) ValidateSession(ctx context.Context) (shopping.SessionIdentity, error) {
	return f(ctx)
}
