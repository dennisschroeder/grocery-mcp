package browserbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func shortSocketPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "grocery-mcp-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, socketFilename)
}

func serveInBackground(t *testing.T, server *Server, ctx context.Context) {
	t.Helper()
	go func() {
		if err := server.Serve(ctx); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()
}

func TestOnlyOneServerCanOwnTheSocket(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := Listen(socketPath); err != ErrServerRunning {
		t.Fatalf("got %v, want %v", err, ErrServerRunning)
	}
}

func TestServerMatchesPollAndResultByRequestID(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveInBackground(t, server, ctx)

	doCtx, doCancel := context.WithTimeout(ctx, 5*time.Second)
	defer doCancel()
	type doOutcome struct {
		payload json.RawMessage
		err     error
	}
	doDone := make(chan doOutcome, 1)
	go func() {
		payload, err := server.Do(doCtx, OperationSessionIdentity, nil)
		doDone <- doOutcome{payload, err}
	}()

	// Act as the native host: poll once to receive the queued operation.
	pollConnection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	pollPayload, err := EncodePollRequest(PollRequest{Type: "poll"})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(pollConnection, pollPayload); err != nil {
		t.Fatal(err)
	}
	responsePayload, err := ReadFrame(pollConnection)
	if err != nil {
		t.Fatal(err)
	}
	pollConnection.Close()
	response, err := DecodePollResponse(responsePayload)
	if err != nil {
		t.Fatal(err)
	}
	if response.Type != "operation" || response.Operation != OperationSessionIdentity || response.RequestID == "" {
		t.Fatalf("unexpected poll response: %#v", response)
	}

	// Deliver the result on a fresh connection, matched by request_id.
	resultConnection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resultConnection.Close()
	resultPayload, err := EncodePollRequest(PollRequest{
		Type:      "result",
		RequestID: response.RequestID,
		OK:        true,
		Result:    json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(resultConnection, resultPayload); err != nil {
		t.Fatal(err)
	}
	ackPayload, err := ReadFrame(resultConnection)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := DecodePollResponse(ackPayload)
	if err != nil || ack.Type != "ack" {
		t.Fatalf("unexpected ack: %#v, err=%v", ack, err)
	}

	select {
	case outcome := <-doDone:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if string(outcome.payload) != `{"ok":true}` {
			t.Fatalf("unexpected Do() result: %s", outcome.payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Do() did not return")
	}
}

func TestPollRespondsIdleAfterTimeoutWithNoQueuedOperation(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server.pollTimeout = 50 * time.Millisecond
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveInBackground(t, server, ctx)

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
	if response.Type != "idle" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestDoRejectsOperationsOutsideTheAllowlist(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_, err = server.Do(t.Context(), Operation("delete_account"), nil)
	var coded CodedError
	if !errors.As(err, &coded) || coded.Code() != "operation_not_allowed" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoReturnsWhenContextIsCanceledBeforeAPollArrives(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = server.Do(ctx, OperationSessionIdentity, nil)
	var coded CodedError
	if !errors.As(err, &coded) || coded.Code() != "canceled" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoMapsAnUnrecognizedFailureCodeToTheSafeFallback(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveInBackground(t, server, ctx)

	doCtx, doCancel := context.WithTimeout(ctx, 5*time.Second)
	defer doCancel()
	type doOutcome struct {
		err error
	}
	doDone := make(chan doOutcome, 1)
	go func() {
		_, err := server.Do(doCtx, OperationProductsSearch, nil)
		doDone <- doOutcome{err}
	}()

	pollConnection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	pollPayload, _ := EncodePollRequest(PollRequest{Type: "poll"})
	if err := WriteFrame(pollConnection, pollPayload); err != nil {
		t.Fatal(err)
	}
	responsePayload, err := ReadFrame(pollConnection)
	if err != nil {
		t.Fatal(err)
	}
	pollConnection.Close()
	response, err := DecodePollResponse(responsePayload)
	if err != nil {
		t.Fatal(err)
	}

	resultConnection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resultConnection.Close()
	resultPayload, _ := EncodePollRequest(PollRequest{
		Type:      "result",
		RequestID: response.RequestID,
		OK:        false,
		Code:      "unexpected-and-unrecognized",
	})
	if err := WriteFrame(resultConnection, resultPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFrame(resultConnection); err != nil {
		t.Fatal(err)
	}

	select {
	case outcome := <-doDone:
		var coded CodedError
		if !errors.As(outcome.err, &coded) || coded.Code() != "operation_failed" {
			t.Fatalf("unexpected error: %v", outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Do() did not return")
	}
}

// TestConcurrentDoCallersEachGetTheirOwnResult proves multiple simultaneous
// MCP tool calls (each becoming its own Do() call) never cross-wire —
// caller A never receives caller B's result. The native host's own poll
// loop is strictly sequential (one operation in flight through the browser
// at a time, per host.go), so this drives N poll/result round trips one
// after another; what's being verified is that concurrent Go-side queuing
// and the mutex-guarded pending map correctly route each result back to the
// goroutine that queued the matching request_id, not just that the server
// doesn't crash under -race.
func TestConcurrentDoCallersEachGetTheirOwnResult(t *testing.T) {
	const callers = 5
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveInBackground(t, server, ctx)

	type doOutcome struct {
		params  string
		payload json.RawMessage
		err     error
	}
	doDone := make(chan doOutcome, callers)
	for i := 0; i < callers; i++ {
		params := json.RawMessage(`{"caller":` + string(rune('0'+i)) + `}`)
		go func() {
			doCtx, doCancel := context.WithTimeout(ctx, 5*time.Second)
			defer doCancel()
			payload, err := server.Do(doCtx, OperationSessionIdentity, params)
			doDone <- doOutcome{params: string(params), payload: payload, err: err}
		}()
	}

	// Act as the native host: its poll loop is sequential, so drive exactly
	// `callers` poll/result round trips, echoing each operation's own params
	// back as its result so mismatched routing is directly observable.
	for i := 0; i < callers; i++ {
		pollConnection, err := net.Dial("unix", socketPath)
		if err != nil {
			t.Fatal(err)
		}
		pollPayload, err := EncodePollRequest(PollRequest{Type: "poll"})
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteFrame(pollConnection, pollPayload); err != nil {
			t.Fatal(err)
		}
		responsePayload, err := ReadFrame(pollConnection)
		if err != nil {
			t.Fatal(err)
		}
		pollConnection.Close()
		response, err := DecodePollResponse(responsePayload)
		if err != nil {
			t.Fatal(err)
		}
		if response.Type != "operation" || response.RequestID == "" {
			t.Fatalf("unexpected poll response: %#v", response)
		}

		resultConnection, err := net.Dial("unix", socketPath)
		if err != nil {
			t.Fatal(err)
		}
		resultPayload, err := EncodePollRequest(PollRequest{
			Type:      "result",
			RequestID: response.RequestID,
			OK:        true,
			Result:    response.Params, // echo the request's own params back
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteFrame(resultConnection, resultPayload); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFrame(resultConnection); err != nil {
			t.Fatal(err)
		}
		resultConnection.Close()
	}

	seen := make(map[string]bool, callers)
	for i := 0; i < callers; i++ {
		select {
		case outcome := <-doDone:
			if outcome.err != nil {
				t.Fatalf("Do() error = %v", outcome.err)
			}
			if string(outcome.payload) != outcome.params {
				t.Fatalf("caller received a mismatched result: sent %s, got %s", outcome.params, outcome.payload)
			}
			if seen[outcome.params] {
				t.Fatalf("params %s delivered to more than one caller", outcome.params)
			}
			seen[outcome.params] = true
		case <-time.After(5 * time.Second):
			t.Fatal("not all Do() calls returned")
		}
	}
	if len(seen) != callers {
		t.Fatalf("saw %d distinct results, want %d", len(seen), callers)
	}
}

func TestRelayDeadlinesLeaveResponseHeadroom(t *testing.T) {
	if defaultPollTimeout >= serverConnectionTimeout || serverConnectionTimeout >= nativeHostResponseTimeout {
		t.Fatalf("deadlines are not ordered: poll=%s server=%s host=%s", defaultPollTimeout, serverConnectionTimeout, nativeHostResponseTimeout)
	}
}
