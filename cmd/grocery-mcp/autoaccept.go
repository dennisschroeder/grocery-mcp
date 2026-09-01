package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/auth"
	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

// autoAcceptingAuthenticator wraps *auth.Service for the live MCP server.
// Outcome B had an inbound push: the extension delivered a credential to a
// listening socket, and the server's connection handler called Accept
// directly as that message arrived. Outcome C inverted the direction — the
// native host polls the server for work — so nothing pushes into Accept
// anymore; something has to proactively drive validation once Connect enters
// Bootstrapping, the way bridge-smoke's CLI harness does manually. This type
// is that missing driver for the real server, with bounded attempts and
// automatic retries.
type autoAcceptingAuthenticator struct {
	ctx     context.Context
	service *auth.Service
	now     func() time.Time

	requestMu           sync.Mutex
	mu                  sync.Mutex
	running             bool
	needsAction         bool
	reauthPending       bool
	disconnectRevision  uint64
	reauthRevision      uint64
	generation          uint64
	driverCancel        context.CancelFunc
	driverDone          chan struct{}
	restartPending      bool
	retryInterval       time.Duration
	reauthRetryInterval time.Duration
	actionDelay         time.Duration
	validationTimeout   time.Duration
}

const (
	automaticRetryInterval       = time.Second
	automaticReauthRetryInterval = 5 * time.Second
	automaticActionDelay         = 30 * time.Second
	automaticValidationTimeout   = 90 * time.Second
)

func newAutoAcceptingAuthenticator(ctx context.Context, service *auth.Service) *autoAcceptingAuthenticator {
	return &autoAcceptingAuthenticator{
		ctx:                 ctx,
		service:             service,
		now:                 time.Now,
		retryInterval:       automaticRetryInterval,
		reauthRetryInterval: automaticReauthRetryInterval,
		actionDelay:         automaticActionDelay,
		validationTimeout:   automaticValidationTimeout,
	}
}

func startAutoAcceptingAuthenticator(ctx context.Context, service *auth.Service) *autoAcceptingAuthenticator {
	authenticator := newAutoAcceptingAuthenticator(ctx, service)
	authenticator.Connect()
	return authenticator
}

func (a *autoAcceptingAuthenticator) Connect() shopping.AuthStatus {
	a.requestMu.Lock()
	defer a.requestMu.Unlock()

	a.mu.Lock()
	disconnectRevision := a.disconnectRevision
	a.mu.Unlock()
	state := a.service.Status().State
	if state == shopping.AuthReauthRequired {
		a.mu.Lock()
		a.reauthPending = true
		a.mu.Unlock()
	}
	if state == shopping.AuthActive {
		a.service.Refresh()
	} else {
		a.service.Connect()
	}
	a.driveAcceptOnce(disconnectRevision)
	return a.Status()
}

func (a *autoAcceptingAuthenticator) Status() shopping.AuthStatus {
	status := a.service.Status()
	a.mu.Lock()
	needsAction := a.needsAction
	reauthPending := a.reauthPending
	a.mu.Unlock()
	if reauthPending && status.State != shopping.AuthActive {
		status.State = shopping.AuthReauthRequired
	}
	status.ActionRequired = status.State == shopping.AuthReauthRequired || needsAction
	return status
}

func (a *autoAcceptingAuthenticator) Disconnect() shopping.AuthStatus {
	a.requestMu.Lock()
	defer a.requestMu.Unlock()

	a.mu.Lock()
	a.disconnectRevision++
	a.reauthRevision++
	a.generation++
	a.needsAction = false
	a.reauthPending = false
	a.restartPending = false
	cancel := a.driverCancel
	done := a.driverDone
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	status := a.service.Disconnect()
	if done != nil {
		<-done
	}
	return status
}

func (a *autoAcceptingAuthenticator) Identity() (shopping.SessionIdentity, bool) {
	return a.service.Identity()
}

func (a *autoAcceptingAuthenticator) RefreshAndValidate(ctx context.Context) error {
	a.mu.Lock()
	disconnectRevision := a.disconnectRevision
	a.mu.Unlock()

	err := a.service.RefreshAndValidate(ctx)
	a.mu.Lock()
	if a.disconnectRevision != disconnectRevision || a.service.Status().State != shopping.AuthReauthRequired {
		a.mu.Unlock()
		return err
	}
	a.reauthPending = true
	a.reauthRevision++
	reauthRevision := a.reauthRevision
	a.mu.Unlock()
	go a.driveAcceptAfterReauthInterval(disconnectRevision, reauthRevision)
	return err
}

func (a *autoAcceptingAuthenticator) driveAcceptAfterReauthInterval(disconnectRevision, reauthRevision uint64) {
	timer := time.NewTimer(a.reauthRetryInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
		a.driveScheduledAccept(disconnectRevision, reauthRevision)
	case <-a.ctx.Done():
	}
}

func (a *autoAcceptingAuthenticator) driveAcceptOnce(expectedDisconnectRevision uint64) {
	a.driveAccept(expectedDisconnectRevision, 0, false)
}

func (a *autoAcceptingAuthenticator) driveScheduledAccept(expectedDisconnectRevision, expectedReauthRevision uint64) {
	a.driveAccept(expectedDisconnectRevision, expectedReauthRevision, true)
}

// driveAccept starts recovery only if no explicit Disconnect or newer reauth
// episode superseded the request that scheduled it.
func (a *autoAcceptingAuthenticator) driveAccept(expectedDisconnectRevision, expectedReauthRevision uint64, checkReauthRevision bool) {
	status := a.service.Status()
	a.mu.Lock()
	if a.disconnectRevision != expectedDisconnectRevision || checkReauthRevision && a.reauthRevision != expectedReauthRevision {
		a.mu.Unlock()
		return
	}
	if !canDriveAccept(status.State) {
		a.mu.Unlock()
		return
	}
	if a.running {
		a.restartPending = true
		a.mu.Unlock()
		return
	}
	a.running = true
	a.needsAction = false
	a.generation++
	generation := a.generation
	driverContext, cancelDriver := context.WithCancel(a.ctx)
	done := make(chan struct{})
	a.driverCancel = cancelDriver
	a.driverDone = done
	a.mu.Unlock()

	go a.runAcceptDriver(driverContext, cancelDriver, done, generation)
}

func (a *autoAcceptingAuthenticator) runAcceptDriver(driverContext context.Context, cancelDriver context.CancelFunc, done chan struct{}, generation uint64) {
	for {
		a.runAcceptGeneration(driverContext, generation)

		a.mu.Lock()
		state := a.service.Status().State
		if state == shopping.AuthActive {
			a.needsAction = false
			a.reauthPending = false
			a.reauthRevision++
		}
		if a.restartPending && canDriveAccept(state) && driverContext.Err() == nil {
			a.restartPending = false
			a.needsAction = false
			a.generation++
			generation = a.generation
			a.mu.Unlock()
			continue
		}
		a.restartPending = false
		a.running = false
		if a.driverDone == done {
			a.driverCancel = nil
			a.driverDone = nil
		}
		close(done)
		a.mu.Unlock()
		cancelDriver()
		return
	}
}

func canDriveAccept(state shopping.AuthState) bool {
	return state == shopping.AuthBootstrapping || state == shopping.AuthRefreshing || state == shopping.AuthReauthRequired
}

func (a *autoAcceptingAuthenticator) runAcceptGeneration(driverContext context.Context, generation uint64) {
	started := a.now()
	for {
		a.mu.Lock()
		current := a.generation
		a.mu.Unlock()
		if current != generation {
			return
		}
		attemptContext, cancelAttempt := context.WithTimeout(driverContext, a.validationTimeout)
		err := a.service.Accept(attemptContext, browserbridge.NewTabBinding(a.now()))
		cancelAttempt()
		if err == nil || driverContext.Err() != nil {
			return
		}
		status := a.service.Status()
		retryInterval := a.retryInterval
		if status.State == shopping.AuthReauthRequired {
			a.mu.Lock()
			if a.generation == generation && driverContext.Err() == nil {
				a.reauthPending = true
			}
			a.mu.Unlock()
			retryInterval = a.reauthRetryInterval
		}
		var coded interface{ Code() string }
		missingPort := errors.As(err, &coded) && coded.Code() == "bridge_unavailable"
		if missingPort && a.now().Sub(started) >= a.actionDelay {
			a.mu.Lock()
			if a.generation == generation && driverContext.Err() == nil {
				a.needsAction = true
			}
			a.mu.Unlock()
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-timer.C:
			if !a.retryConnect(generation) {
				return
			}
		case <-driverContext.Done():
			timer.Stop()
			return
		}
	}
}

func (a *autoAcceptingAuthenticator) retryConnect(generation uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.generation != generation {
		return false
	}
	a.service.Connect()
	return true
}
