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
	"strings"
	"sync"
	"time"
)

const (
	defaultPollTimeout      = 25 * time.Second
	serverConnectionTimeout = defaultPollTimeout + 5*time.Second
	operationResultTimeout  = nativeHostResponseTimeout + socketDialTimeout + serverConnectionTimeout + time.Second
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
	requestID  string
	operation  Operation
	params     json.RawMessage
	result     chan pollResult
	dispatched chan struct{}
}

type pollResult struct {
	ok     bool
	code   string
	result json.RawMessage
}

type Server struct {
	listener         net.Listener
	socketPath       string
	statePath        string
	removeDirectory  bool
	closeOnce        sync.Once
	pollTimeout      time.Duration
	operationTimeout time.Duration

	queue chan *pendingOperation
	slot  chan struct{}

	mu              sync.Mutex
	pending         map[string]*pendingOperation
	peers           map[string]context.CancelFunc
	sharedState     map[string]json.RawMessage
	activeRequestID string
}

func Listen(socketPath string) (*Server, error) {
	removeDirectory, err := prepareSocketPath(socketPath)
	if err != nil {
		return nil, err
	}
	return listenPrepared(socketPath, removeDirectory)
}

func listenPrepared(socketPath string, removeDirectory bool) (*Server, error) {
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
	statePath := filepath.Join(filepath.Dir(socketPath), sharedStateFilename)
	sharedState, err := loadSharedState(statePath)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, err
	}
	server := &Server{
		listener:         listener,
		socketPath:       socketPath,
		statePath:        statePath,
		removeDirectory:  removeDirectory,
		pollTimeout:      defaultPollTimeout,
		operationTimeout: operationResultTimeout,
		queue:            make(chan *pendingOperation),
		slot:             make(chan struct{}, 1),
		pending:          make(map[string]*pendingOperation),
		peers:            make(map[string]context.CancelFunc),
		sharedState:      sharedState,
	}
	server.slot <- struct{}{}
	return server, nil
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
	messageType, err := decodeMessageType(payload)
	if err != nil {
		return
	}
	if messageType == "call" || messageType == "cancel" {
		request, err := DecodePeerRequest(payload)
		if err != nil {
			return
		}
		if request.Type == "call" {
			s.handlePeerCall(ctx, connection, request)
			return
		}
		s.handlePeerCancel(request)
		s.writePeerResponse(connection, PeerResponse{Type: "ack", RequestID: request.RequestID, OK: true})
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

func (s *Server) handlePeerCall(ctx context.Context, connection net.Conn, request *PeerRequest) {
	_ = connection.SetDeadline(time.Time{})
	callContext, cancel := context.WithCancel(ctx)
	defer cancel()

	s.mu.Lock()
	if _, exists := s.peers[request.RequestID]; exists {
		s.mu.Unlock()
		s.writePeerResponse(connection, PeerResponse{
			Type:      "call_result",
			RequestID: request.RequestID,
			Code:      "duplicate_request",
		})
		return
	}
	s.peers[request.RequestID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.peers, request.RequestID)
		s.mu.Unlock()
	}()

	type outcome struct {
		result json.RawMessage
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := s.doPeerCall(callContext, request.Operation, request.Params)
		done <- outcome{result: result, err: err}
	}()

	disconnected := make(chan struct{}, 1)
	go func() {
		_, _ = ReadFrame(connection)
		disconnected <- struct{}{}
	}()

	select {
	case result := <-done:
		response := PeerResponse{Type: "call_result", RequestID: request.RequestID, OK: result.err == nil, Result: result.result}
		if result.err != nil {
			response.Code = errorCode(result.err)
		}
		s.writePeerResponse(connection, response)
	case <-disconnected:
		cancel()
		<-done
	}
}

func (s *Server) doPeerCall(ctx context.Context, op Operation, params json.RawMessage) (json.RawMessage, error) {
	switch op {
	case operationSharedStateGet, operationSharedStatePut:
		return s.doSharedState(op, params)
	default:
		return s.Do(ctx, op, params)
	}
}

type sharedStateGetParams struct {
	Key string `json:"key"`
}

type sharedStatePutParams struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type sharedStateResult struct {
	Found bool            `json:"found"`
	Value json.RawMessage `json:"value,omitempty"`
}

func (s *Server) doSharedState(op Operation, params json.RawMessage) (json.RawMessage, error) {
	switch op {
	case operationSharedStateGet:
		var request sharedStateGetParams
		if err := strictDecode(params, &request); err != nil || !validSharedStateKey(request.Key) {
			return nil, &bridgeError{code: "invalid_params"}
		}
		s.mu.Lock()
		value, found := s.sharedState[request.Key]
		value = append(json.RawMessage(nil), value...)
		s.mu.Unlock()
		result, err := json.Marshal(sharedStateResult{Found: found, Value: value})
		if err != nil {
			return nil, &bridgeError{code: "internal_error"}
		}
		return result, nil
	case operationSharedStatePut:
		var request sharedStatePutParams
		if err := strictDecode(params, &request); err != nil || !validSharedStateKey(request.Key) || len(request.Value) == 0 || !json.Valid(request.Value) {
			return nil, &bridgeError{code: "invalid_params"}
		}
		s.mu.Lock()
		previous, existed := s.sharedState[request.Key]
		s.sharedState[request.Key] = append(json.RawMessage(nil), request.Value...)
		if err := writeSharedState(s.statePath, s.sharedState); err != nil {
			if existed {
				s.sharedState[request.Key] = previous
			} else {
				delete(s.sharedState, request.Key)
			}
			s.mu.Unlock()
			return nil, &bridgeError{code: "internal_error"}
		}
		s.mu.Unlock()
		return nil, nil
	default:
		return nil, &bridgeError{code: "operation_not_allowed"}
	}
}

func validSharedStateKey(key string) bool {
	if len(key) == 0 || len(key) > 128 || !strings.HasPrefix(key, "shopping:") || len(key) == len("shopping:") {
		return false
	}
	for _, character := range key {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) handlePeerCancel(request *PeerRequest) {
	s.mu.Lock()
	cancel := s.peers[request.RequestID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) handlePoll(ctx context.Context, connection net.Conn) {
	pollContext, cancel := context.WithTimeout(ctx, s.pollTimeout)
	defer cancel()
	select {
	case <-s.slot:
		select {
		case operation := <-s.queue:
			s.mu.Lock()
			s.pending[operation.requestID] = operation
			s.activeRequestID = operation.requestID
			close(operation.dispatched)
			s.mu.Unlock()
			go s.expireOperation(ctx, operation.requestID)
			s.writeResponse(connection, PollResponse{
				Type:      "operation",
				RequestID: operation.requestID,
				Operation: operation.operation,
				Params:    operation.params,
			})
		case <-pollContext.Done():
			s.releaseSlot()
			s.writeResponse(connection, PollResponse{Type: "idle"})
		}
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
	active := s.activeRequestID == request.RequestID
	if active {
		s.activeRequestID = ""
	}
	s.mu.Unlock()
	if active {
		s.releaseSlot()
	}
	if !ok {
		return
	}
	result := normalizePollResult(pollResult{ok: request.OK, code: request.Code, result: request.Result})
	select {
	case operation.result <- result:
	default:
	}
}

func (s *Server) expireOperation(ctx context.Context, requestID string) {
	timer := time.NewTimer(s.operationTimeout)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return
	}

	s.mu.Lock()
	if s.activeRequestID != requestID {
		s.mu.Unlock()
		return
	}
	s.activeRequestID = ""
	operation, pending := s.pending[requestID]
	delete(s.pending, requestID)
	s.mu.Unlock()
	s.releaseSlot()
	if pending {
		select {
		case operation.result <- pollResult{code: "content_script_unreachable"}:
		default:
		}
	}
}

func normalizePollResult(result pollResult) pollResult {
	if result.ok {
		result.code = ""
	} else {
		result.code = safeErrorCode(result.code)
		result.result = nil
	}
	payload, err := EncodePeerResponse(PeerResponse{
		Type:      "call_result",
		RequestID: "00000000000000000000000000000000",
		OK:        result.ok,
		Code:      result.code,
		Result:    result.result,
	})
	if err != nil || len(payload) > maxMessageBytes {
		return pollResult{code: "malformed_response"}
	}
	return result
}

func (s *Server) releaseSlot() {
	select {
	case s.slot <- struct{}{}:
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

func (s *Server) writePeerResponse(connection net.Conn, response PeerResponse) {
	payload, err := EncodePeerResponse(response)
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
	if !operationFrameFits(requestID, op, params) {
		return nil, &bridgeError{code: "invalid_params"}
	}
	operation := &pendingOperation{
		requestID:  requestID,
		operation:  op,
		params:     params,
		result:     make(chan pollResult, 1),
		dispatched: make(chan struct{}),
	}

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
		<-operation.dispatched
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

func validRequestID(requestID string) bool {
	if len(requestID) != requestIDEncodedBytes {
		return false
	}
	_, err := hex.DecodeString(requestID)
	return err == nil
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

func errorCode(err error) string {
	var coded CodedError
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return "operation_failed"
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
