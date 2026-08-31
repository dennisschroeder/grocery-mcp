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

const (
	runtimeBaseDirectory = "/tmp"
	socketFilename       = "bridge.sock"
	lockFilename         = "bridge.lock"
)

var ErrServerRunning = errors.New("browser bridge server is already running")

func DefaultSocketPath() string {
	return filepath.Join(runtimeBaseDirectory, "grocery-mcp-"+strconv.Itoa(os.Getuid()), socketFilename)
}

func prepareSocketPath(path string) (bool, error) {
	created, err := prepareRuntimeDirectory(path)
	if err != nil {
		return false, err
	}
	if err := prepareOwnerSocketPath(path); err != nil {
		return false, err
	}
	return created, nil
}

func prepareRuntimeDirectory(path string) (bool, error) {
	if !filepath.IsAbs(path) || filepath.Base(path) != socketFilename {
		return false, fmt.Errorf("bridge socket path is invalid")
	}
	directory := filepath.Dir(path)
	created := false
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		mkdirErr := os.Mkdir(directory, 0o700)
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return false, fmt.Errorf("create bridge runtime directory: %w", mkdirErr)
		}
		created = mkdirErr == nil
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
	return created, nil
}

func prepareOwnerSocketPath(path string) error {
	if socketInfo, socketErr := os.Lstat(path); socketErr == nil {
		if socketInfo.Mode()&os.ModeSymlink != 0 || socketInfo.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("bridge socket path is occupied by an unsafe file")
		}
		connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return ErrServerRunning
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale bridge socket: %w", err)
		}
	} else if !errors.Is(socketErr, os.ErrNotExist) {
		return fmt.Errorf("inspect bridge socket: %w", socketErr)
	}
	return nil
}

func acquireOwnerLock(socketPath string) (*os.File, bool, error) {
	lockPath := filepath.Join(filepath.Dir(socketPath), lockFilename)
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	fd, err := syscall.Open(lockPath, flags|syscall.O_CREAT|syscall.O_EXCL, 0o600)
	if errors.Is(err, syscall.EEXIST) {
		fd, err = syscall.Open(lockPath, flags, 0)
	}
	if err != nil {
		return nil, false, fmt.Errorf("open bridge owner lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	valid := false
	defer func() {
		if !valid {
			_ = lock.Close()
		}
	}()

	info, err := lock.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("inspect bridge owner lock: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || int(stat.Uid) != os.Getuid() || info.Mode().Perm() != 0o600 {
		return nil, false, fmt.Errorf("bridge owner lock is unsafe")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock bridge owner file: %w", err)
	}
	valid = true
	return lock, true, nil
}

func releaseOwnerLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
}
