package main

import (
	"context"
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
// is that missing driver for the real server, one background attempt per
// Connect call.
type autoAcceptingAuthenticator struct {
	ctx     context.Context
	service *auth.Service
	now     func() time.Time

	mu      sync.Mutex
	running bool
}

func newAutoAcceptingAuthenticator(ctx context.Context, service *auth.Service) *autoAcceptingAuthenticator {
	return &autoAcceptingAuthenticator{ctx: ctx, service: service, now: time.Now}
}

func (a *autoAcceptingAuthenticator) Connect() shopping.AuthStatus {
	status := a.service.Connect()
	a.driveAcceptOnce()
	return status
}

func (a *autoAcceptingAuthenticator) Status() shopping.AuthStatus {
	return a.service.Status()
}

func (a *autoAcceptingAuthenticator) Disconnect() shopping.AuthStatus {
	return a.service.Disconnect()
}

func (a *autoAcceptingAuthenticator) Identity() (shopping.SessionIdentity, bool) {
	return a.service.Identity()
}

func (a *autoAcceptingAuthenticator) RefreshAndValidate(ctx context.Context) error {
	return a.service.RefreshAndValidate(ctx)
}

// driveAcceptOnce starts at most one in-flight Accept per Connect call.
// Accept itself is responsible for rejecting a stale attempt (via its
// requestRevision check) if a newer Connect/Disconnect superseded this one
// before the browser answered; the error is discarded here because the
// service's own state, not this goroutine's return value, is what auth_status
// reports.
func (a *autoAcceptingAuthenticator) driveAcceptOnce() {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return
	}
	a.running = true
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.running = false
			a.mu.Unlock()
		}()
		_ = a.service.Accept(a.ctx, browserbridge.NewTabBinding(a.now()))
	}()
}
