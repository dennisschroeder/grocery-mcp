package browserbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const sharedStateFilename = "state.json"

func lockSharedState(ctx context.Context, directory, key string) (func(), error) {
	if !validSharedStateKey(key) {
		return nil, fmt.Errorf("invalid shared state key")
	}
	digest := sha256.Sum256([]byte(key))
	path := filepath.Join(directory, fmt.Sprintf("state-%x.lock", digest))
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open shared state lock: %w", err)
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
				_ = lock.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = lock.Close()
			return nil, fmt.Errorf("lock shared state: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = lock.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func loadSharedState(path string) (map[string]json.RawMessage, error) {
	values := make(map[string]json.RawMessage)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect bridge state: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || int(stat.Uid) != os.Getuid() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("bridge state file is unsafe")
	}
	if info.Size() > maxMessageBytes {
		return nil, fmt.Errorf("bridge state file is too large")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read bridge state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("decode bridge state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("decode bridge state: trailing payload")
	}
	for key, value := range values {
		if !validSharedStateKey(key) || len(value) == 0 || !json.Valid(value) {
			return nil, fmt.Errorf("bridge state file is invalid")
		}
	}
	return values, nil
}

func writeSharedState(path string, values map[string]json.RawMessage) error {
	payload, err := json.Marshal(values)
	if err != nil || len(payload) > maxMessageBytes {
		return fmt.Errorf("encode bridge state")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return fmt.Errorf("create bridge state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure bridge state: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write bridge state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close bridge state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace bridge state: %w", err)
	}
	return nil
}
