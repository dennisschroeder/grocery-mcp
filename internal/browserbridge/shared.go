package browserbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

const (
	electionRetryInterval = 10 * time.Millisecond
	electionRetryBudget   = 3 * time.Second
)

// SharedTransport elects one process to own the browser bridge while all
// other processes forward calls to that owner through the same Unix socket.
type SharedTransport struct {
	ctx        context.Context
	cancel     context.CancelFunc
	socketPath string
	client     *Client

	election sync.Mutex
	mu       sync.Mutex
	owner    *sharedOwner
	closed   bool
}

type sharedOwner struct {
	server *Server
	lock   *os.File
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func OpenShared(ctx context.Context, socketPath string) (*SharedTransport, error) {
	sharedContext, cancel := context.WithCancel(ctx)
	transport := &SharedTransport{
		ctx:        sharedContext,
		cancel:     cancel,
		socketPath: socketPath,
		client:     NewClient(socketPath),
	}
	if _, err := prepareRuntimeDirectory(socketPath); err != nil {
		cancel()
		return nil, err
	}
	if _, err := transport.tryBecomeOwner(); err != nil {
		cancel()
		return nil, err
	}
	return transport, nil
}

func (t *SharedTransport) Do(ctx context.Context, op Operation, params json.RawMessage) (json.RawMessage, error) {
	callContext, cancel := context.WithCancel(ctx)
	stopTransport := context.AfterFunc(t.ctx, cancel)
	defer func() {
		stopTransport()
		cancel()
	}()

	deadline := time.Now().Add(electionRetryBudget)
	for {
		owner := t.currentOwner()
		if owner != nil {
			return doWithOwnerContext(callContext, owner, op, params)
		}

		result, err := t.client.Do(callContext, op, params)
		if !retrySafeUnavailable(err) {
			return result, err
		}
		becameOwner, electionErr := t.tryBecomeOwner()
		if electionErr != nil {
			return nil, &bridgeError{code: "bridge_unavailable"}
		}
		if becameOwner {
			owner = t.currentOwner()
			if owner == nil {
				return nil, &bridgeError{code: "bridge_unavailable"}
			}
			return doWithOwnerContext(callContext, owner, op, params)
		}
		if time.Now().After(deadline) {
			return nil, &bridgeError{code: "bridge_unavailable"}
		}
		if !sleepOrDone(callContext, electionRetryInterval) {
			if callContext.Err() != nil {
				return nil, &bridgeError{code: "canceled"}
			}
			return nil, &bridgeError{code: "bridge_unavailable"}
		}
	}
}

func doWithOwnerContext(ctx context.Context, owner *sharedOwner, op Operation, params json.RawMessage) (json.RawMessage, error) {
	callContext, cancel := context.WithCancel(ctx)
	stopOwner := context.AfterFunc(owner.ctx, cancel)
	defer func() {
		stopOwner()
		cancel()
	}()
	return owner.server.Do(callContext, op, params)
}

func (t *SharedTransport) tryBecomeOwner() (bool, error) {
	t.election.Lock()
	defer t.election.Unlock()
	if t.currentOwner() != nil {
		return true, nil
	}

	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return false, &bridgeError{code: "bridge_unavailable"}
	}

	lock, acquired, err := acquireOwnerLock(t.socketPath)
	if err != nil || !acquired {
		return false, err
	}
	if err := prepareOwnerSocketPath(t.socketPath); err != nil {
		releaseOwnerLock(lock)
		return false, err
	}
	server, err := listenPrepared(t.socketPath, false)
	if err != nil {
		releaseOwnerLock(lock)
		return false, err
	}
	ownerContext, cancelOwner := context.WithCancel(t.ctx)
	owner := &sharedOwner{server: server, lock: lock, ctx: ownerContext, cancel: cancelOwner, done: make(chan struct{})}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		cancelOwner()
		_ = server.Close()
		releaseOwnerLock(lock)
		return false, &bridgeError{code: "bridge_unavailable"}
	}
	t.owner = owner
	t.mu.Unlock()

	go t.serveOwner(ownerContext, owner)
	return true, nil
}

func (t *SharedTransport) serveOwner(ctx context.Context, owner *sharedOwner) {
	_ = owner.server.Serve(ctx)
	owner.cancel()
	_ = owner.server.Close()

	t.mu.Lock()
	if t.owner == owner {
		t.owner = nil
	}
	t.mu.Unlock()
	releaseOwnerLock(owner.lock)
	close(owner.done)
}

func (t *SharedTransport) currentOwner() *sharedOwner {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.owner
}

func (t *SharedTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	owner := t.owner
	t.mu.Unlock()

	t.cancel()
	if owner == nil {
		return nil
	}
	owner.cancel()
	err := owner.server.Close()
	<-owner.done
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func retrySafeUnavailable(err error) bool {
	var clientErr *clientError
	return errors.As(err, &clientErr) && clientErr.code == "bridge_unavailable" && clientErr.retrySafe
}

var _ Transport = (*SharedTransport)(nil)
