package browserbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func TestRunNativeHostRejectsOriginBeforeReadingInput(t *testing.T) {
	err := RunNativeHost(t.Context(), "chrome-extension://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/", "chrome-extension://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/", "unused", bytes.NewReader(nil), io.Discard)
	if err == nil {
		t.Fatal("expected origin rejection")
	}
}

// TestNativeHostReconnectsAfterTheSocketComesBack proves a restarting MCP
// server doesn't strand the native host: RunNativeHost starts against a
// socket path with nothing listening yet, keeps retrying, and picks up a
// queued operation once a server finally starts listening — all without the
// caller (standing in for the Chrome port) ever seeing a disconnect.
func TestNativeHostReconnectsAfterTheSocketComesBack(t *testing.T) {
	originalInterval := reconnectRetryInterval
	reconnectRetryInterval = 5 * time.Millisecond
	t.Cleanup(func() { reconnectRetryInterval = originalInterval })

	socketPath := shortSocketPath(t)
	origin := "chrome-extension://abcdefghijklmnopabcdefghijklmnop/"
	hostInput, chromeOutput := io.Pipe()
	chromeInput, hostOutput := io.Pipe()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hostDone := make(chan error, 1)
	go func() {
		hostDone <- RunNativeHost(ctx, origin, origin, socketPath, hostInput, hostOutput)
	}()

	// Give RunNativeHost time to hit several failed dial attempts against
	// the not-yet-listening socket before the server ever starts.
	time.Sleep(50 * time.Millisecond)

	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server.pollTimeout = 200 * time.Millisecond
	defer server.Close()
	serveInBackground(t, server, ctx)

	extensionDone := make(chan struct{})
	go func() {
		defer close(extensionDone)
		payload, err := ReadFrame(chromeInput)
		if err != nil {
			t.Errorf("extension read operation_request: %v", err)
			return
		}
		request, err := DecodePortMessage(payload)
		if err != nil {
			t.Errorf("extension decode operation_request: %v", err)
			return
		}
		response, err := EncodePortMessage(PortMessage{
			Type:      "operation_response",
			RequestID: request.RequestID,
			OK:        true,
			Result:    json.RawMessage(`{"ok":true}`),
		})
		if err != nil {
			t.Errorf("extension encode operation_response: %v", err)
			return
		}
		if err := WriteFrame(chromeOutput, response); err != nil {
			t.Errorf("extension write operation_response: %v", err)
		}
	}()

	doCtx, doCancel := context.WithTimeout(ctx, 5*time.Second)
	defer doCancel()
	result, err := server.Do(doCtx, OperationSessionIdentity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("unexpected result: %s", result)
	}

	select {
	case <-extensionDone:
	case <-time.After(5 * time.Second):
		t.Fatal("extension goroutine did not complete")
	}

	cancel()
	select {
	case err := <-hostDone:
		if err != nil {
			t.Fatalf("native host returned an error on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native host did not stop")
	}
}

func TestNativeHostKeepsRetryingUntilShutdown(t *testing.T) {
	originalInterval := reconnectRetryInterval
	reconnectRetryInterval = 5 * time.Millisecond
	t.Cleanup(func() { reconnectRetryInterval = originalInterval })

	socketPath := shortSocketPath(t)
	origin := "chrome-extension://abcdefghijklmnopabcdefghijklmnop/"
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	hostInput, chromeOutput := io.Pipe()
	defer hostInput.Close()
	defer chromeOutput.Close()

	start := time.Now()
	err := RunNativeHost(ctx, origin, origin, socketPath, hostInput, io.Discard)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("native host returned an error on shutdown: %v", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("gave up after %v while Chrome's port was still open", elapsed)
	}
}

func TestNativeHostStopsWhenChromeClosesDuringReconnect(t *testing.T) {
	originalInterval := reconnectRetryInterval
	reconnectRetryInterval = time.Second
	t.Cleanup(func() { reconnectRetryInterval = originalInterval })

	socketPath := shortSocketPath(t)
	origin := "chrome-extension://abcdefghijklmnopabcdefghijklmnop/"
	hostInput, chromeOutput := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunNativeHost(t.Context(), origin, origin, socketPath, hostInput, io.Discard)
	}()
	if err := chromeOutput.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("native host returned an error after Chrome closed: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("native host kept retrying after Chrome closed its port")
	}
}

func TestReconnectWaitDiscardsALateOperationResponse(t *testing.T) {
	payload, err := EncodePortMessage(PortMessage{
		Type:      "operation_response",
		RequestID: "request-1",
		OK:        true,
		Result:    json.RawMessage(`{"late":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := make(chan portFrame, 1)
	input <- portFrame{payload: payload}

	closed, err := waitForRetry(t.Context(), time.Second, input)
	if err != nil || closed {
		t.Fatalf("waitForRetry() = closed %v, err %v", closed, err)
	}
}

func TestRelayOperationKeepsChromePortOpenWhenOwnerDiesBeforeResultPost(t *testing.T) {
	reply, err := EncodePortMessage(PortMessage{Type: "operation_response", RequestID: "request-1", OK: true, Result: json.RawMessage(`{"ok":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	input := make(chan portFrame, 1)
	input <- portFrame{payload: reply}
	closed, err := relayOperation(t.Context(), shortSocketPath(t), input, io.Discard, &PollResponse{RequestID: "request-1", Operation: OperationSessionIdentity})
	if err != nil || closed {
		t.Fatalf("relayOperation() = closed %v, err %v", closed, err)
	}
}

func TestNativeHostSurvivesTimedOutOperationAndDiscardsItsLateResponse(t *testing.T) {
	originalTimeout := operationResponseTimeout
	operationResponseTimeout = 20 * time.Millisecond
	t.Cleanup(func() { operationResponseTimeout = originalTimeout })

	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server.pollTimeout = 50 * time.Millisecond
	server.dispatchTimeout = 100 * time.Millisecond
	server.operationTimeout = 100 * time.Millisecond
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveInBackground(t, server, ctx)

	hostInput, chromeOutput := io.Pipe()
	chromeInput, hostOutput := io.Pipe()
	origin := "chrome-extension://abcdefghijklmnopabcdefghijklmnop/"
	hostDone := make(chan error, 1)
	go func() {
		hostDone <- RunNativeHost(ctx, origin, origin, socketPath, hostInput, hostOutput)
	}()

	extensionDone := make(chan error, 1)
	go func() {
		firstPayload, err := ReadFrame(chromeInput)
		if err != nil {
			extensionDone <- err
			return
		}
		first, err := DecodePortMessage(firstPayload)
		if err != nil {
			extensionDone <- err
			return
		}
		time.Sleep(40 * time.Millisecond)
		late, _ := EncodePortMessage(PortMessage{Type: "operation_response", RequestID: first.RequestID, OK: true, Result: json.RawMessage(`{"late":true}`)})
		if err := WriteFrame(chromeOutput, late); err != nil {
			extensionDone <- err
			return
		}

		secondPayload, err := ReadFrame(chromeInput)
		if err != nil {
			extensionDone <- err
			return
		}
		second, err := DecodePortMessage(secondPayload)
		if err != nil {
			extensionDone <- err
			return
		}
		reply, _ := EncodePortMessage(PortMessage{Type: "operation_response", RequestID: second.RequestID, OK: true, Result: json.RawMessage(`{"ok":true}`)})
		extensionDone <- WriteFrame(chromeOutput, reply)
	}()

	_, err = server.Do(ctx, OperationBasketApply, json.RawMessage(`{"changes":[{"listing_id":"item-1","quantity":1}]}`))
	var coded CodedError
	if !errors.As(err, &coded) || coded.Code() != "operation_timeout" {
		t.Fatalf("timed-out operation error = %v, want operation_timeout", err)
	}

	select {
	case err := <-hostDone:
		t.Fatalf("native host exited after one slow operation: %v", err)
	default:
	}

	result, err := server.Do(ctx, OperationSessionIdentity, nil)
	if err != nil {
		t.Fatalf("operation after timeout failed: %v", err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("operation after timeout result = %s", result)
	}
	if err := <-extensionDone; err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-hostDone:
		if err != nil {
			t.Fatalf("native host shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native host did not stop")
	}
}

func TestNativeHostDrivesAQueuedOperationThroughTheChromePort(t *testing.T) {
	socketPath := shortSocketPath(t)
	server, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server.pollTimeout = 200 * time.Millisecond
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	serveInBackground(t, server, ctx)

	// hostInput/chromeOutput and chromeInput/hostOutput simulate the two
	// directions of the persistent Chrome Native Messaging port.
	hostInput, chromeOutput := io.Pipe()
	chromeInput, hostOutput := io.Pipe()
	origin := "chrome-extension://abcdefghijklmnopabcdefghijklmnop/"

	hostDone := make(chan error, 1)
	go func() {
		hostDone <- RunNativeHost(ctx, origin, origin, socketPath, hostInput, hostOutput)
	}()

	extensionDone := make(chan struct{})
	go func() {
		defer close(extensionDone)
		payload, err := ReadFrame(chromeInput)
		if err != nil {
			t.Errorf("extension read operation_request: %v", err)
			return
		}
		request, err := DecodePortMessage(payload)
		if err != nil {
			t.Errorf("extension decode operation_request: %v", err)
			return
		}
		if request.Type != "operation_request" || request.Operation != OperationSessionIdentity {
			t.Errorf("unexpected operation_request: %#v", request)
			return
		}
		response, err := EncodePortMessage(PortMessage{
			Type:      "operation_response",
			RequestID: request.RequestID,
			OK:        true,
			Result:    json.RawMessage(`{"favoriteLists":{"favorites":[{"id":"list-123"}]}}`),
		})
		if err != nil {
			t.Errorf("extension encode operation_response: %v", err)
			return
		}
		if err := WriteFrame(chromeOutput, response); err != nil {
			t.Errorf("extension write operation_response: %v", err)
		}
	}()

	doCtx, doCancel := context.WithTimeout(ctx, 5*time.Second)
	defer doCancel()
	result, err := server.Do(doCtx, OperationSessionIdentity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"favoriteLists":{"favorites":[{"id":"list-123"}]}}` {
		t.Fatalf("unexpected result: %s", result)
	}

	select {
	case <-extensionDone:
	case <-time.After(5 * time.Second):
		t.Fatal("extension goroutine did not complete")
	}

	// Cancel the shared context, as the process shutdown path would; the
	// native host should stop cleanly rather than error out.
	cancel()
	select {
	case err := <-hostDone:
		if err != nil {
			t.Fatalf("native host returned an error on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native host did not stop")
	}
}
