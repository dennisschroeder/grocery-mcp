package browserbridge

import (
	"context"
	"encoding/json"
	"net"
	"time"
)

// Client forwards browser operations to the grocery-mcp process that owns
// the browser bridge socket.
type Client struct {
	socketPath string
}

type clientError struct {
	code      string
	retrySafe bool
}

func (e *clientError) Error() string { return "browser bridge operation failed" }
func (e *clientError) Code() string  { return e.code }

func NewClient(socketPath string) *Client {
	return &Client{socketPath: socketPath}
}

func (c *Client) Do(ctx context.Context, op Operation, params json.RawMessage) (json.RawMessage, error) {
	if !op.allowlisted() {
		return nil, &bridgeError{code: "operation_not_allowed"}
	}
	requestID, err := newRequestID()
	if err != nil {
		return nil, &bridgeError{code: "internal_error"}
	}
	request := PeerRequest{Type: "call", RequestID: requestID, Operation: op, Params: params}
	payload, err := EncodePeerRequest(request)
	if err != nil {
		return nil, &bridgeError{code: "internal_error"}
	}
	if len(payload) == 0 || len(payload) > maxMessageBytes || !operationFrameFits(requestID, op, params) {
		return nil, &bridgeError{code: "invalid_params"}
	}
	connection, err := dialSocket(ctx, c.socketPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &bridgeError{code: "canceled"}
		}
		return nil, &clientError{code: "bridge_unavailable", retrySafe: true}
	}
	defer connection.Close()

	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if err := WriteFrame(connection, payload); err != nil {
		if ctx.Err() != nil {
			return nil, &bridgeError{code: "canceled"}
		}
		return nil, &clientError{code: "ambiguous_result"}
	}

	responsePayload, err := ReadFrame(connection)
	if err != nil {
		if ctx.Err() != nil {
			c.cancel(requestID)
			return nil, &bridgeError{code: "canceled"}
		}
		return nil, &clientError{code: "ambiguous_result"}
	}
	response, err := DecodePeerResponse(responsePayload)
	if err != nil || response.Type != "call_result" || response.RequestID != requestID {
		return nil, &clientError{code: "ambiguous_result"}
	}
	if !response.OK {
		return nil, &bridgeError{code: safePeerErrorCode(response.Code)}
	}
	return response.Result, nil
}

func (c *Client) cancel(requestID string) {
	connection, err := net.DialTimeout("unix", c.socketPath, time.Second)
	if err != nil {
		return
	}
	defer connection.Close()
	payload, err := EncodePeerRequest(PeerRequest{Type: "cancel", RequestID: requestID})
	if err != nil {
		return
	}
	_ = WriteFrame(connection, payload)
}

func safePeerErrorCode(code string) string {
	switch code {
	case "operation_not_allowed", "canceled", "ambiguous_result", "internal_error", "duplicate_request":
		return code
	default:
		return safeErrorCode(code)
	}
}

var _ Transport = (*Client)(nil)
