package browserbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	return t.route(ctx,
		func(ctx context.Context, owner *sharedOwner) (json.RawMessage, error) {
			return doWithOwnerContext(ctx, owner, op, params)
		},
		func(ctx context.Context) (json.RawMessage, error) {
			return t.client.Do(ctx, op, params)
		},
	)
}

func (t *SharedTransport) route(
	ctx context.Context,
	ownerCall func(context.Context, *sharedOwner) (json.RawMessage, error),
	peerCall func(context.Context) (json.RawMessage, error),
) (json.RawMessage, error) {
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
			return ownerCall(callContext, owner)
		}

		result, err := peerCall(callContext)
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
			return ownerCall(callContext, owner)
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

// LoadState and StoreState share small, account-scoped coordination values
// between serve processes through the elected owner. The owner persists this
// non-auth coordination state in its private runtime directory for failover.
func (t *SharedTransport) LoadState(ctx context.Context, key string) (json.RawMessage, bool, error) {
	params, err := json.Marshal(sharedStateGetParams{Key: key})
	if err != nil {
		return nil, false, &bridgeError{code: "internal_error"}
	}
	result, err := t.route(ctx,
		func(_ context.Context, owner *sharedOwner) (json.RawMessage, error) {
			return owner.server.doSharedState(operationSharedStateGet, params)
		},
		func(ctx context.Context) (json.RawMessage, error) {
			return t.client.loadState(ctx, key)
		},
	)
	if err != nil {
		return nil, false, err
	}
	var state sharedStateResult
	if err := strictDecode(result, &state); err != nil {
		return nil, false, &bridgeError{code: "ambiguous_result"}
	}
	return append(json.RawMessage(nil), state.Value...), state.Found, nil
}

func (t *SharedTransport) StoreState(ctx context.Context, key string, value json.RawMessage) error {
	params, err := json.Marshal(sharedStatePutParams{Key: key, Value: value})
	if err != nil {
		return &bridgeError{code: "internal_error"}
	}
	_, err = t.route(ctx,
		func(_ context.Context, owner *sharedOwner) (json.RawMessage, error) {
			return owner.server.doSharedState(operationSharedStatePut, params)
		},
		func(ctx context.Context) (json.RawMessage, error) {
			return nil, t.client.storeState(ctx, key, value)
		},
	)
	if err != nil {
		return err
	}
	return nil
}

func (t *SharedTransport) LockState(ctx context.Context, key string) (func(), error) {
	return lockSharedState(ctx, filepath.Dir(t.socketPath), key)
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
