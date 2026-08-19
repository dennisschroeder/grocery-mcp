package browserbridge

import (
	"bytes"
	"context"
	"encoding/json"
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
	originalInterval, originalBudget := reconnectRetryInterval, reconnectRetryBudget
	reconnectRetryInterval = 5 * time.Millisecond
	reconnectRetryBudget = 2 * time.Second
	t.Cleanup(func() { reconnectRetryInterval, reconnectRetryBudget = originalInterval, originalBudget })

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

// TestNativeHostGivesUpAfterTheRetryBudgetExpires proves a persistently
// unreachable socket still eventually surfaces as an error (and process
// exit) rather than retrying forever in silence.
func TestNativeHostGivesUpAfterTheRetryBudgetExpires(t *testing.T) {
	originalInterval, originalBudget := reconnectRetryInterval, reconnectRetryBudget
	reconnectRetryInterval = 5 * time.Millisecond
	reconnectRetryBudget = 30 * time.Millisecond
	t.Cleanup(func() { reconnectRetryInterval, reconnectRetryBudget = originalInterval, originalBudget })

	socketPath := shortSocketPath(t)
	origin := "chrome-extension://abcdefghijklmnopabcdefghijklmnop/"

	start := time.Now()
	err := RunNativeHost(t.Context(), origin, origin, socketPath, bytes.NewReader(nil), io.Discard)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error once the retry budget expired")
	}
	if elapsed < reconnectRetryBudget {
		t.Fatalf("gave up after %v, want at least the retry budget (%v)", elapsed, reconnectRetryBudget)
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
