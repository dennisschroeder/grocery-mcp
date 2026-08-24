package browserbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	nativeHostResponseTimeout = defaultPollTimeout + 10*time.Second
	socketDialTimeout         = 3 * time.Second
)

// reconnectRetryInterval and reconnectRetryBudget bound how RunNativeHost
// reacts to the bridge socket itself being unreachable (the MCP server
// process restarting) as opposed to the Chrome port closing (which
// relayOperation's EOF check handles separately). Overridable by tests via
// package vars, matching Server.pollTimeout's pattern, since there's no
// per-call options struct here.
var (
	reconnectRetryInterval = 2 * time.Second
	reconnectRetryBudget   = 60 * time.Second
)

// RunNativeHost drives the poll loop for as long as the Chrome port (input,
// output) stays open: dial the socket, poll, and on a queued operation relay
// it down the port, wait for the matching answer, report the result back to
// the socket, then loop. It returns nil when the port closes cleanly.
//
// A dial/poll failure against the socket (the MCP server process restarting,
// not the Chrome port closing) does not exit the loop immediately: the port
// itself is still open and Chrome has no reason to relaunch this process, so
// exiting here would silently strand the extension until the user re-clicks
// the action. Instead this retries for up to reconnectRetryBudget, giving a
// restarting server time to come back up without the extension noticing.
// Past that budget, a persistently broken socket is treated the same as
// before: return the error, let the process exit, surface it as a badge.
func RunNativeHost(ctx context.Context, origin, allowedOrigin, socketPath string, input io.Reader, output io.Writer) error {
	if origin == "" || origin != allowedOrigin || !strings.HasPrefix(origin, "chrome-extension://") {
		return fmt.Errorf("native host origin is not allowed")
	}
	var unreachableSince time.Time
	for {
		response, err := pollOnce(ctx, socketPath)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if unreachableSince.IsZero() {
				unreachableSince = time.Now()
			} else if time.Since(unreachableSince) > reconnectRetryBudget {
				return err
			}
			if !sleepOrDone(ctx, reconnectRetryInterval) {
				return nil
			}
			continue
		}
		unreachableSince = time.Time{}
		if response.Type != "operation" {
			continue
		}
		closed, err := relayOperation(ctx, socketPath, input, output, response)
		if err != nil {
			return err
		}
		if closed {
			return nil
		}
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func pollOnce(ctx context.Context, socketPath string) (*PollResponse, error) {
	connection, err := dialSocket(ctx, socketPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("dial bridge socket: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(nativeHostResponseTimeout))
	payload, err := EncodePollRequest(PollRequest{Type: "poll"})
	if err != nil {
		return nil, err
	}
	if err := WriteFrame(connection, payload); err != nil {
		return nil, fmt.Errorf("send poll request: %w", err)
	}
	responsePayload, err := ReadFrame(connection)
	if err != nil {
		return nil, fmt.Errorf("read poll response: %w", err)
	}
	return DecodePollResponse(responsePayload)
}

// relayOperation forwards a queued operation down the Chrome port, waits for
// the matching operation_response, and reports the outcome back to the
// socket. portClosed is true only when the port read hit a clean EOF.
func relayOperation(ctx context.Context, socketPath string, input io.Reader, output io.Writer, poll *PollResponse) (portClosed bool, err error) {
	request := PortMessage{
		Type:      "operation_request",
		RequestID: poll.RequestID,
		Operation: poll.Operation,
		Params:    poll.Params,
	}
	requestPayload, err := EncodePortMessage(request)
	if err != nil {
		return false, err
	}
	if err := WriteFrame(output, requestPayload); err != nil {
		return false, fmt.Errorf("write operation request: %w", err)
	}

	replyPayload, err := readFrameWithTimeout(ctx, input, nativeHostResponseTimeout)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read operation response: %w", err)
	}
	reply, err := DecodePortMessage(replyPayload)
	if err != nil {
		return false, err
	}
	if reply.Type != "operation_response" || reply.RequestID != poll.RequestID {
		return false, fmt.Errorf("operation response does not match the pending request")
	}

	return false, postResult(ctx, socketPath, PollRequest{
		Type:      "result",
		RequestID: poll.RequestID,
		OK:        reply.OK,
		Code:      reply.Code,
		Result:    reply.Result,
	})
}

func postResult(ctx context.Context, socketPath string, result PollRequest) error {
	connection, err := dialSocket(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("dial bridge socket: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(nativeHostResponseTimeout))
	payload, err := EncodePollRequest(result)
	if err != nil {
		return err
	}
	if err := WriteFrame(connection, payload); err != nil {
		return fmt.Errorf("send result: %w", err)
	}
	ackPayload, err := ReadFrame(connection)
	if err != nil {
		return fmt.Errorf("read result ack: %w", err)
	}
	_, err = DecodePollResponse(ackPayload)
	return err
}

func dialSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: socketDialTimeout}
	return dialer.DialContext(ctx, "unix", socketPath)
}

// readFrameWithTimeout wraps ReadFrame with a deadline for readers (like
// os.Stdin in tests) that don't support SetReadDeadline. A timeout leaves
// the underlying read goroutine running; it is reclaimed when the reader
// eventually yields data or closes.
func readFrameWithTimeout(ctx context.Context, reader io.Reader, timeout time.Duration) ([]byte, error) {
	type readResult struct {
		payload []byte
		err     error
	}
	done := make(chan readResult, 1)
	go func() {
		payload, err := ReadFrame(reader)
		done <- readResult{payload, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.payload, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("timed out waiting for operation response")
	}
}
