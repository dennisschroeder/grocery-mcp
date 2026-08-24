package browserbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const (
	protocolVersion       = 2
	peerProtocolVersion   = 1
	requestIDEncodedBytes = 32
)

type Operation string

const (
	OperationSessionIdentity Operation = "session_identity"
	OperationStoresSearch    Operation = "stores_search"
	OperationProductsSearch  Operation = "products_search"
	OperationBasketGet       Operation = "basket_get"
	OperationBasketApply     Operation = "basket_apply"
	OperationTimeslotsList   Operation = "timeslots_list"
	OperationTimeslotReserve Operation = "timeslot_reserve"
	OperationOrdersList      Operation = "orders_list"
	OperationOrderGet        Operation = "order_get"
)

// allowlisted is the Go-side operation check. It is defense in depth only —
// the content script's fixed switch on the operation string is the real
// security boundary, since it decides what actually runs in the REWE tab.
func (op Operation) allowlisted() bool {
	switch op {
	case OperationSessionIdentity, OperationStoresSearch, OperationProductsSearch,
		OperationBasketGet, OperationBasketApply, OperationTimeslotsList, OperationTimeslotReserve,
		OperationOrdersList, OperationOrderGet:
		return true
	default:
		return false
	}
}

// PortMessage is exchanged on the persistent Chrome Native Messaging port
// between the extension and the native host.
type PortMessage struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Operation Operation       `json:"operation,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	OK        bool            `json:"ok,omitempty"`
	Code      string          `json:"code,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}

// PollRequest is sent by the native host to the MCP server over the Unix
// socket: "poll" asks for queued work, "result" delivers an answer.
type PollRequest struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	OK        bool            `json:"ok,omitempty"`
	Code      string          `json:"code,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}

// PollResponse is sent by the MCP server back to the native host: "operation"
// carries queued work, "idle" is a poll-timeout with nothing queued, "ack"
// confirms a delivered result.
type PollResponse struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Operation Operation       `json:"operation,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// PeerRequest is exchanged between grocery-mcp processes over the bridge
// socket. The owner executes "call" requests through the same queue as its
// local callers; "cancel" stops the call with the matching request ID.
type PeerRequest struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Operation Operation       `json:"operation,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// PeerResponse returns a peer call result or acknowledges cancellation.
type PeerResponse struct {
	Version   int             `json:"version"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok,omitempty"`
	Code      string          `json:"code,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}

func DecodePortMessage(data []byte) (*PortMessage, error) {
	var message PortMessage
	if err := strictDecode(data, &message); err != nil {
		return nil, err
	}
	if message.Version != protocolVersion {
		return nil, fmt.Errorf("unsupported bridge message")
	}
	return &message, nil
}

func EncodePortMessage(message PortMessage) ([]byte, error) {
	message.Version = protocolVersion
	return json.Marshal(message)
}

func DecodePollRequest(data []byte) (*PollRequest, error) {
	var request PollRequest
	if err := strictDecode(data, &request); err != nil {
		return nil, err
	}
	if request.Version != protocolVersion {
		return nil, fmt.Errorf("unsupported bridge message")
	}
	return &request, nil
}

func EncodePollRequest(request PollRequest) ([]byte, error) {
	request.Version = protocolVersion
	return json.Marshal(request)
}

func DecodePollResponse(data []byte) (*PollResponse, error) {
	var response PollResponse
	if err := strictDecode(data, &response); err != nil {
		return nil, err
	}
	if response.Version != protocolVersion {
		return nil, fmt.Errorf("unsupported bridge message")
	}
	return &response, nil
}

func EncodePollResponse(response PollResponse) ([]byte, error) {
	response.Version = protocolVersion
	return json.Marshal(response)
}

func DecodePeerRequest(data []byte) (*PeerRequest, error) {
	var request PeerRequest
	if err := strictDecode(data, &request); err != nil {
		return nil, err
	}
	if request.Version != peerProtocolVersion {
		return nil, fmt.Errorf("unsupported bridge message")
	}
	if request.Type != "call" && request.Type != "cancel" {
		return nil, fmt.Errorf("unsupported bridge message")
	}
	if request.RequestID == "" {
		return nil, fmt.Errorf("invalid bridge message")
	}
	if !validRequestID(request.RequestID) {
		return nil, fmt.Errorf("invalid bridge message")
	}
	if request.Type == "call" && request.Operation == "" {
		return nil, fmt.Errorf("invalid bridge message")
	}
	if request.Type == "cancel" && (request.Operation != "" || len(request.Params) != 0) {
		return nil, fmt.Errorf("invalid bridge message")
	}
	return &request, nil
}

func EncodePeerRequest(request PeerRequest) ([]byte, error) {
	request.Version = peerProtocolVersion
	return json.Marshal(request)
}

func DecodePeerResponse(data []byte) (*PeerResponse, error) {
	var response PeerResponse
	if err := strictDecode(data, &response); err != nil {
		return nil, err
	}
	if response.Version != peerProtocolVersion {
		return nil, fmt.Errorf("unsupported bridge message")
	}
	if response.Type != "call_result" && response.Type != "ack" {
		return nil, fmt.Errorf("unsupported bridge message")
	}
	if response.RequestID == "" {
		return nil, fmt.Errorf("invalid bridge message")
	}
	if !validRequestID(response.RequestID) {
		return nil, fmt.Errorf("invalid bridge message")
	}
	if response.Type == "ack" && (!response.OK || response.Code != "" || len(response.Result) != 0) {
		return nil, fmt.Errorf("invalid bridge message")
	}
	if response.Type == "call_result" {
		if response.OK && response.Code != "" {
			return nil, fmt.Errorf("invalid bridge message")
		}
		if !response.OK && (response.Code == "" || len(response.Result) != 0) {
			return nil, fmt.Errorf("invalid bridge message")
		}
	}
	return &response, nil
}

func EncodePeerResponse(response PeerResponse) ([]byte, error) {
	response.Version = peerProtocolVersion
	return json.Marshal(response)
}

func operationFrameFits(requestID string, operation Operation, params json.RawMessage) bool {
	pollPayload, err := EncodePollResponse(PollResponse{
		Type:      "operation",
		RequestID: requestID,
		Operation: operation,
		Params:    params,
	})
	if err != nil || len(pollPayload) > maxMessageBytes {
		return false
	}
	portPayload, err := EncodePortMessage(PortMessage{
		Type:      "operation_request",
		RequestID: requestID,
		Operation: operation,
		Params:    params,
	})
	return err == nil && len(portPayload) <= maxMessageBytes
}

func decodeMessageType(data []byte) (string, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Type == "" {
		return "", fmt.Errorf("invalid bridge message")
	}
	return envelope.Type, nil
}

func strictDecode[T any](data []byte, target *T) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid bridge message")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("invalid bridge message")
	}
	return nil
}
