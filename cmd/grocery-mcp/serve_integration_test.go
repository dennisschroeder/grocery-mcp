package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestTwoServeProcessesInitializeAndRemainActive(t *testing.T) {
	runtimeDirectory, err := os.MkdirTemp("/tmp", "grocery-mcp-serve-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDirectory) })
	first := startServeProcess(t, runtimeDirectory)
	defer first.stop(t)
	second := startServeProcess(t, runtimeDirectory)
	defer second.stop(t)

	first.initialize(t, 1)
	second.initialize(t, 2)
	for index, process := range []*serveProcess{first, second} {
		if err := process.command.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("serve process %d stopped after initialize: %v; stderr: %s", index+1, err, process.stderr.String())
		}
	}
}

func TestServeProcessHelper(t *testing.T) {
	if os.Getenv("GROCERY_MCP_SERVE_HELPER") != "1" {
		return
	}
	if err := serveMCP(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type serveProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  bytes.Buffer
}

func startServeProcess(t *testing.T, runtimeDirectory string) *serveProcess {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestServeProcessHelper$")
	command.Env = append(os.Environ(),
		"GROCERY_MCP_SERVE_HELPER=1",
		"XDG_RUNTIME_DIR="+runtimeDirectory,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	process := &serveProcess{command: command, stdin: stdin, stdout: bufio.NewReader(stdout)}
	command.Stderr = &process.stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return process
}

func (p *serveProcess) initialize(t *testing.T, id int) {
	t.Helper()
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "integration-test", "version": "1"},
		},
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.stdin.Write(append(payload, '\n')); err != nil {
		t.Fatalf("write initialize: %v; stderr: %s", err, p.stderr.String())
	}

	type readResult struct {
		line []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		line, err := p.stdout.ReadBytes('\n')
		done <- readResult{line, err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("read initialize response: %v; stderr: %s", result.err, p.stderr.String())
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(result.line, &response); err != nil {
			t.Fatalf("decode initialize response %q: %v", result.line, err)
		}
		if response.ID != id || len(response.Result) == 0 || len(response.Error) != 0 {
			t.Fatalf("unexpected initialize response: %s", result.line)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("initialize timed out; stderr: %s", p.stderr.String())
	}
}

func (p *serveProcess) stop(t *testing.T) {
	t.Helper()
	_ = p.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- p.command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve process exit: %v; stderr: %s", err, p.stderr.String())
		}
	case <-time.After(2 * time.Second):
		_ = p.command.Process.Kill()
		<-done
		t.Errorf("serve process did not stop after stdin closed; stderr: %s", p.stderr.String())
	}
}
