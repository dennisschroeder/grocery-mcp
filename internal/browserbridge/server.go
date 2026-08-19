package browserbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	defaultPollTimeout      = 25 * time.Second
	serverConnectionTimeout = defaultPollTimeout + 5*time.Second
)

// Transport is the narrow surface internal callers (auth.Service, later
// ShoppingCore) need to drive a browser-executed operation. *Server
// satisfies it; callers depend on this instead of the concrete type.
type Transport interface {
	Do(ctx context.Context, op Operation, params json.RawMessage) (json.RawMessage, error)
}

type CodedError interface {
	error
	Code() string
}

type bridgeError struct {
	code string
}

func (e *bridgeError) Error() string { return "browser bridge operation failed" }
func (e *bridgeError) Code() string  { return e.code }

type pendingOperation struct {
	requestID string
	operation Operation
	params    json.RawMessage
	result    chan pollResult
}

type pollResult struct {
	ok     bool
	code   string
	result json.RawMessage
}

type Server struct {
	listener        net.Listener
	socketPath      string
	removeDirectory bool
	closeOnce       sync.Once
	pollTimeout     time.Duration

	queue chan *pendingOperation

	mu      sync.Mutex
	pending map[string]*pendingOperation
}

func Listen(socketPath string) (*Server, error) {
	removeDirectory, err := prepareSocketPath(socketPath)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		if removeDirectory {
			_ = os.Remove(filepath.Dir(socketPath))
		}
		return nil, err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, err
	}
	return &Server{
		listener:        listener,
		socketPath:      socketPath,
		removeDirectory: removeDirectory,
		pollTimeout:     defaultPollTimeout,
		queue:           make(chan *pendingOperation),
		pending:         make(map[string]*pendingOperation),
	}, nil
}

func (s *Server) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(ctx, connection)
	}
}

func (s *Server) handle(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(serverConnectionTimeout))
	payload, err := ReadFrame(connection)
	if err != nil {
		return
	}
	request, err := DecodePollRequest(payload)
	if err != nil {
		s.writeResponse(connection, PollResponse{Type: "idle"})
		return
	}
	switch request.Type {
	case "poll":
		s.handlePoll(ctx, connection)
	case "result":
		s.handleResult(request)
		s.writeResponse(connection, PollResponse{Type: "ack"})
	default:
		s.writeResponse(connection, PollResponse{Type: "idle"})
	}
}

func (s *Server) handlePoll(ctx context.Context, connection net.Conn) {
	pollContext, cancel := context.WithTimeout(ctx, s.pollTimeout)
	defer cancel()
	select {
	case operation := <-s.queue:
		s.mu.Lock()
		s.pending[operation.requestID] = operation
		s.mu.Unlock()
		s.writeResponse(connection, PollResponse{
			Type:      "operation",
			RequestID: operation.requestID,
			Operation: operation.operation,
			Params:    operation.params,
		})
	case <-pollContext.Done():
		s.writeResponse(connection, PollResponse{Type: "idle"})
	}
}

func (s *Server) handleResult(request *PollRequest) {
	s.mu.Lock()
	operation, ok := s.pending[request.RequestID]
	if ok {
		delete(s.pending, request.RequestID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	select {
	case operation.result <- pollResult{ok: request.OK, code: request.Code, result: request.Result}:
	default:
	}
}

func (s *Server) writeResponse(connection net.Conn, response PollResponse) {
	payload, err := EncodePollResponse(response)
	if err != nil {
		return
	}
	_ = WriteFrame(connection, payload)
}

// Do queues op for the next poll and blocks until the native host relays a
// matching result or ctx is canceled first. There is at most one operation
// in flight per queued request; concurrent callers each wait their turn
// behind the next poll connection that arrives.
func (s *Server) Do(ctx context.Context, op Operation, params json.RawMessage) (json.RawMessage, error) {
	if !op.allowlisted() {
		return nil, &bridgeError{code: "operation_not_allowed"}
	}
	requestID, err := newRequestID()
	if err != nil {
		return nil, &bridgeError{code: "internal_error"}
	}
	operation := &pendingOperation{requestID: requestID, operation: op, params: params, result: make(chan pollResult, 1)}

	select {
	case s.queue <- operation:
	case <-ctx.Done():
		return nil, &bridgeError{code: "canceled"}
	}

	select {
	case result := <-operation.result:
		if !result.ok {
			return nil, &bridgeError{code: safeErrorCode(result.code)}
		}
		return result.result, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
		return nil, &bridgeError{code: "canceled"}
	}
}

func newRequestID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

// safeErrorCode bounds a wire-supplied failure code to exactly the vocabulary
// the extension side can produce — content-script.js (auth_invalid,
// rate_limited, upstream_changed, malformed_response, unknown_operation,
// invalid_params, not_implemented) and service-worker.js
// (content_script_unreachable, when the bound tab closes or navigates away
// mid-operation) — it isn't trusted verbatim since it arrives from a
// different codebase built against the same contract, not compiled against
// these Go constants.
func safeErrorCode(code string) string {
	switch code {
	case "not_implemented", "auth_invalid", "rate_limited", "upstream_changed", "malformed_response", "unknown_operation", "invalid_params", "content_script_unreachable":
		return code
	default:
		return "operation_failed"
	}
}

func (s *Server) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		closeErr = s.listener.Close()
		if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) && closeErr == nil {
			closeErr = err
		}
		if s.removeDirectory {
			if err := os.Remove(filepath.Dir(s.socketPath)); err != nil && !errors.Is(err, os.ErrNotExist) && closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}
