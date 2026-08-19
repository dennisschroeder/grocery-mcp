package browserbridge

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
)

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

func TestOperationAllowlist(t *testing.T) {
	for _, op := range []Operation{
		OperationSessionIdentity, OperationStoresSearch, OperationProductsSearch,
		OperationBasketGet, OperationBasketApply, OperationTimeslotsList, OperationTimeslotReserve,
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
