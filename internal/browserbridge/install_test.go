package browserbridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallNativeHostUsesExactOrigin(t *testing.T) {
	home := t.TempDir()
	binaryPath := filepath.Join(home, "bin", "grocery-mcp")
	path, err := InstallNativeHost(home, "darwin", binaryPath, "abcdefghijklmnopabcdefghijklmnop")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := LoadAllowedOrigin(home, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if allowed != "chrome-extension://abcdefghijklmnopabcdefghijklmnop/" {
		t.Fatalf("unexpected origin %q", allowed)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode is %o", info.Mode().Perm())
	}
}

func TestInstallNativeHostRejectsInvalidIdentity(t *testing.T) {
	if _, err := InstallNativeHost(t.TempDir(), "darwin", "relative", "invalid"); err == nil {
		t.Fatal("expected invalid install to fail")
	}
}
