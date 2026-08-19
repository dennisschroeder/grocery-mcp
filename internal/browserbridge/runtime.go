package browserbridge

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const socketFilename = "bridge.sock"

var ErrServerRunning = errors.New("browser bridge server is already running")

func DefaultSocketPath() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "grocery-mcp-"+strconv.Itoa(os.Getuid()), socketFilename)
}

func prepareSocketPath(path string) (bool, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != socketFilename {
		return false, fmt.Errorf("bridge socket path is invalid")
	}
	directory := filepath.Dir(path)
	created := false
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return false, fmt.Errorf("create bridge runtime directory: %w", err)
		}
		created = true
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return false, fmt.Errorf("inspect bridge runtime directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("bridge runtime path is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return false, fmt.Errorf("bridge runtime directory is not owned by the current user")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("bridge runtime directory permissions are too broad")
	}

	if socketInfo, socketErr := os.Lstat(path); socketErr == nil {
		if socketInfo.Mode()&os.ModeSymlink != 0 || socketInfo.Mode()&os.ModeSocket == 0 {
			return false, fmt.Errorf("bridge socket path is occupied by an unsafe file")
		}
		connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return false, ErrServerRunning
		}
		if err := os.Remove(path); err != nil {
			return false, fmt.Errorf("remove stale bridge socket: %w", err)
		}
	} else if !errors.Is(socketErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect bridge socket: %w", socketErr)
	}
	return created, nil
}
