package browserbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

const testPeerRequestID = "00112233445566778899aabbccddeeff"

func TestPortMessageRoundTrips(t *testing.T) {
	message := PortMessage{Type: "operation_request", RequestID: "req-1", Operation: OperationSessionIdentity, Params: json.RawMessage(`{}`)}
	payload, err := EncodePortMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePortMessage(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RequestID != message.RequestID || decoded.Operation != message.Operation || decoded.Type != message.Type {
		t.Fatalf("unexpected round trip: %#v", decoded)
	}
}

func TestPortMessageDecodeIsStrict(t *testing.T) {
	valid, err := EncodePortMessage(PortMessage{Type: "operation_request", RequestID: "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePortMessage(valid); err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{
		"unknown field": []byte(`{"version":2,"type":"operation_request","request_id":"req-1","extra":true}`),
		"trailing":      append(append([]byte{}, valid...), []byte(` {}`)...),
		"wrong version": []byte(`{"version":1,"type":"operation_request","request_id":"req-1"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePortMessage(payload); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestPollRequestRoundTrips(t *testing.T) {
	request := PollRequest{Type: "result", RequestID: "req-1", OK: true, Result: json.RawMessage(`{"id":1}`)}
	payload, err := EncodePollRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePollRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Type != request.Type || decoded.RequestID != request.RequestID || !decoded.OK {
		t.Fatalf("unexpected round trip: %#v", decoded)
	}
}

func TestPollRequestDecodeIsStrict(t *testing.T) {
	valid, err := EncodePollRequest(PollRequest{Type: "poll"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePollRequest(valid); err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{
		"unknown field": []byte(`{"version":2,"type":"poll","extra":true}`),
		"trailing":      append(append([]byte{}, valid...), []byte(` {}`)...),
		"wrong version": []byte(`{"version":1,"type":"poll"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePollRequest(payload); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestPollResponseRoundTrips(t *testing.T) {
	response := PollResponse{Type: "operation", RequestID: "req-1", Operation: OperationSessionIdentity}
	payload, err := EncodePollResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePollResponse(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Type != response.Type || decoded.Operation != response.Operation {
		t.Fatalf("unexpected round trip: %#v", decoded)
	}
}

func TestPollResponseDecodeIsStrict(t *testing.T) {
	valid, err := EncodePollResponse(PollResponse{Type: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePollResponse(valid); err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{
		"unknown field": []byte(`{"version":2,"type":"idle","extra":true}`),
		"trailing":      append(append([]byte{}, valid...), []byte(` {}`)...),
		"wrong version": []byte(`{"version":1,"type":"idle"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePollResponse(payload); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestPeerMessagesRoundTrip(t *testing.T) {
	request := PeerRequest{Type: "call", RequestID: testPeerRequestID, Operation: OperationBasketGet, Params: json.RawMessage(`{"store_id":"1"}`)}
	payload, err := EncodePeerRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	decodedRequest, err := DecodePeerRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decodedRequest.RequestID != request.RequestID || decodedRequest.Operation != request.Operation {
		t.Fatalf("unexpected request: %#v", decodedRequest)
	}

	response := PeerResponse{Type: "call_result", RequestID: request.RequestID, OK: true, Result: json.RawMessage(`{"ok":true}`)}
	payload, err = EncodePeerResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	decodedResponse, err := DecodePeerResponse(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decodedResponse.RequestID != response.RequestID || !decodedResponse.OK {
		t.Fatalf("unexpected response: %#v", decodedResponse)
	}
}

func TestPeerMessageDecodeIsStrictAndVersioned(t *testing.T) {
	valid, err := EncodePeerRequest(PeerRequest{Type: "call", RequestID: testPeerRequestID, Operation: OperationSessionIdentity})
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{
		"unknown field":          []byte(`{"version":1,"type":"call","request_id":"00112233445566778899aabbccddeeff","operation":"session_identity","extra":true}`),
		"trailing":               append(append([]byte{}, valid...), []byte(` {}`)...),
		"wrong version":          []byte(`{"version":2,"type":"call","request_id":"00112233445566778899aabbccddeeff","operation":"session_identity"}`),
		"missing id":             []byte(`{"version":1,"type":"call","operation":"session_identity"}`),
		"invalid id":             []byte(`{"version":1,"type":"call","request_id":"peer-1","operation":"session_identity"}`),
		"unknown type":           []byte(`{"version":1,"type":"retry","request_id":"00112233445566778899aabbccddeeff"}`),
		"call without operation": []byte(`{"version":1,"type":"call","request_id":"00112233445566778899aabbccddeeff"}`),
		"cancel with operation":  []byte(`{"version":1,"type":"cancel","request_id":"00112233445566778899aabbccddeeff","operation":"basket_get"}`),
		"cancel with params":     []byte(`{"version":1,"type":"cancel","request_id":"00112233445566778899aabbccddeeff","params":{}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePeerRequest(payload); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestPeerResponseRejectsContradictoryFields(t *testing.T) {
	for name, payload := range map[string][]byte{
		"success with code":    []byte(`{"version":1,"type":"call_result","request_id":"00112233445566778899aabbccddeeff","ok":true,"code":"canceled"}`),
		"failure without code": []byte(`{"version":1,"type":"call_result","request_id":"00112233445566778899aabbccddeeff"}`),
		"failure with result":  []byte(`{"version":1,"type":"call_result","request_id":"00112233445566778899aabbccddeeff","code":"canceled","result":{}}`),
		"ack without ok":       []byte(`{"version":1,"type":"ack","request_id":"00112233445566778899aabbccddeeff"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePeerResponse(payload); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestOperationFrameLimitUsesLargestDownstreamEnvelope(t *testing.T) {
	params := operationEnvelopeBoundaryParams(t)
	peerPayload, err := EncodePeerRequest(PeerRequest{
		Type:      "call",
		RequestID: testPeerRequestID,
		Operation: OperationBasketApply,
		Params:    params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(peerPayload) > maxMessageBytes {
		t.Fatal("test input already exceeds the peer envelope")
	}
	if operationFrameFits(testPeerRequestID, OperationBasketApply, params) {
		t.Fatal("native port envelope overflow was accepted")
	}
}

func operationEnvelopeBoundaryParams(t *testing.T) json.RawMessage {
	t.Helper()
	for size := maxMessageBytes; size > maxMessageBytes-256; size-- {
		params := json.RawMessage(`{"value":"` + strings.Repeat("x", size) + `"}`)
		peerPayload, _ := EncodePeerRequest(PeerRequest{
			Type:      "call",
			RequestID: testPeerRequestID,
			Operation: OperationBasketApply,
			Params:    params,
		})
		pollPayload, _ := EncodePollResponse(PollResponse{
			Type:      "operation",
			RequestID: testPeerRequestID,
			Operation: OperationBasketApply,
			Params:    params,
		})
		portPayload, _ := EncodePortMessage(PortMessage{
			Type:      "operation_request",
			RequestID: testPeerRequestID,
			Operation: OperationBasketApply,
			Params:    params,
		})
		if len(peerPayload) <= maxMessageBytes && len(pollPayload) <= maxMessageBytes && len(portPayload) > maxMessageBytes {
			return params
		}
	}
	t.Fatal("did not find the poll/native-port envelope boundary")
	return nil
}

func TestOperationAllowlist(t *testing.T) {
	for _, op := range []Operation{
		OperationSessionIdentity, OperationStoresSearch, OperationProductsSearch,
		OperationBasketGet, OperationBasketDiscover, OperationBasketListingsGet, OperationBasketApply, OperationTimeslotsList, OperationTimeslotReserve,
		OperationOrdersList, OperationOrderGet,
	} {
		if !op.allowlisted() {
			t.Fatalf("%s should be allowlisted", op)
		}
	}
	if Operation("delete_account").allowlisted() {
		t.Fatal("unlisted operation was accepted")
	}
}

type shortWriter struct {
	buffer bytes.Buffer
}

func (w *shortWriter) Write(data []byte) (int, error) {
	if len(data) > 2 {
		data = data[:2]
	}
	return w.buffer.Write(data)
}

func TestFrameRoundTripHandlesShortWrites(t *testing.T) {
	payload := []byte(`{"ok":true}`)
	writer := &shortWriter{}
	if err := WriteFrame(writer, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&writer.buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

func TestReadFrameRejectsOversizedMessage(t *testing.T) {
	var buffer bytes.Buffer
	var header [4]byte
	binary.NativeEndian.PutUint32(header[:], maxMessageBytes+1)
	buffer.Write(header[:])
	if _, err := ReadFrame(&buffer); err == nil {
		t.Fatal("expected oversized frame rejection")
	}
}
