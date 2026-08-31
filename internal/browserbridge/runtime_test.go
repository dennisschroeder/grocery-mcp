package browserbridge

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestDefaultSocketPathIgnoresProcessRuntimeEnvironment(t *testing.T) {
	want := filepath.Join(runtimeBaseDirectory, "grocery-mcp-"+strconv.Itoa(os.Getuid()), socketFilename)

	t.Setenv("TMPDIR", "/tmp/claude-surface")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/claude-runtime")
	first := DefaultSocketPath()

	t.Setenv("TMPDIR", "/tmp/chrome-surface")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/chrome-runtime")
	second := DefaultSocketPath()

	if first != want || second != want {
		t.Fatalf("DefaultSocketPath() = %q and %q, want %q", first, second, want)
	}
}
