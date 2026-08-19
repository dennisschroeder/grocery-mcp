package browserbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const protocolVersion = 2

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
