package browserbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestClientCallUsesOwnerQueueAndReturnsMatchingResult(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveInBackground(t, server, ctx)

	client := NewClient(socketPath)
	done := make(chan struct {
		result json.RawMessage
		err    error
	}, 1)
	go func() {
		result, err := client.Do(ctx, OperationProductsSearch, json.RawMessage(`{"query":"milk"}`))
		done <- struct {
			result json.RawMessage
			err    error
		}{result, err}
	}()

	operation := pollOperation(t, socketPath)
	if operation.Operation != OperationProductsSearch || string(operation.Params) != `{"query":"milk"}` {
		t.Fatalf("unexpected operation: %#v", operation)
	}
	postOperationResult(t, socketPath, operation.RequestID, true, "", json.RawMessage(`{"products":[]}`))

	select {
	case outcome := <-done:
		if outcome.err != nil || string(outcome.result) != `{"products":[]}` {
			t.Fatalf("result = %s, err = %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("peer call did not return")
	}
}

func TestConcurrentClientsReceiveOnlyTheirMatchingResults(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveInBackground(t, server, ctx)

	type outcome struct {
		params string
		result json.RawMessage
		err    error
	}
	done := make(chan outcome, 2)
	for _, params := range []string{`{"caller":"cowork"}`, `{"caller":"code"}`} {
		go func(params string) {
			result, err := NewClient(socketPath).Do(ctx, OperationProductsSearch, json.RawMessage(params))
			done <- outcome{params: params, result: result, err: err}
		}(params)
	}

	for range 2 {
		operation := pollOperation(t, socketPath)
		postOperationResult(t, socketPath, operation.RequestID, true, "", operation.Params)
	}
	for range 2 {
		select {
		case result := <-done:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if string(result.result) != result.params {
				t.Fatalf("caller sent %s, got %s", result.params, result.result)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("not all peer calls returned")
		}
	}
}

func TestClientCancellationRemovesOwnerPendingRequest(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serverContext, stopServer := context.WithCancel(t.Context())
	defer stopServer()
	serveInBackground(t, server, serverContext)

	callContext, cancelCall := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := NewClient(socketPath).Do(callContext, OperationBasketGet, nil)
		done <- err
	}()
	operation := pollOperation(t, socketPath)
	cancelCall()

	select {
	case err := <-done:
		var coded CodedError
		if !errors.As(err, &coded) || coded.Code() != "canceled" {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled peer call did not return")
	}

	postOperationResult(t, socketPath, operation.RequestID, true, "", json.RawMessage(`{"late":true}`))
	server.mu.Lock()
	_, stillPending := server.pending[operation.RequestID]
	server.mu.Unlock()
	if stillPending {
		t.Fatal("canceled operation remained pending")
	}
}

func TestClientMarksDisconnectAfterSendAsAmbiguous(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		if _, err := ReadFrame(connection); err == nil {
			close(received)
		}
	}()

	_, err = NewClient(socketPath).Do(t.Context(), OperationBasketApply, json.RawMessage(`{"items":[]}`))
	<-received
	var coded CodedError
	if !errors.As(err, &coded) || coded.Code() != "ambiguous_result" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRejectsOversizeCallBeforeDial(t *testing.T) {
	params := json.RawMessage(`{"value":"` + strings.Repeat("x", maxMessageBytes) + `"}`)
	_, err := NewClient("/path/that/does/not/exist/bridge.sock").Do(t.Context(), OperationBasketApply, params)
	var coded CodedError
	if !errors.As(err, &coded) || coded.Code() != "invalid_params" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientRejectsNativeEnvelopeOverflowBeforeDial(t *testing.T) {
	_, err := NewClient("/path/that/does/not/exist/bridge.sock").Do(t.Context(), OperationBasketApply, operationEnvelopeBoundaryParams(t))
	var coded CodedError
	if !errors.As(err, &coded) || coded.Code() != "invalid_params" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func pollOperation(t *testing.T, socketPath string) *PollResponse {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	payload, err := EncodePollRequest(PollRequest{Type: "poll"})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(connection, payload); err != nil {
		t.Fatal(err)
	}
	responsePayload, err := ReadFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	response, err := DecodePollResponse(responsePayload)
	if err != nil {
		t.Fatal(err)
	}
	if response.Type != "operation" || response.RequestID == "" {
		t.Fatalf("unexpected poll response: %#v", response)
	}
	return response
}

func postOperationResult(t *testing.T, socketPath, requestID string, ok bool, code string, result json.RawMessage) {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	payload, err := EncodePollRequest(PollRequest{Type: "result", RequestID: requestID, OK: ok, Code: code, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(connection, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFrame(connection); err != nil {
		t.Fatal(err)
	}
}
