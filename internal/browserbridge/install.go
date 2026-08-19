package browserbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const NativeHostName = "com.dennisschroeder.grocery_mcp"

var extensionIDPattern = regexp.MustCompile(`^[a-p]{32}$`)

type NativeHostManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

func InstallNativeHost(home, goos, binaryPath, extensionID string) (string, error) {
	if !filepath.IsAbs(binaryPath) {
		return "", fmt.Errorf("native host binary path must be absolute")
	}
	if !extensionIDPattern.MatchString(extensionID) {
		return "", fmt.Errorf("Chrome extension ID is invalid")
	}
	manifestPath, err := NativeHostManifestPath(home, goos)
	if err != nil {
		return "", err
	}
	manifest := NativeHostManifest{
		Name:           NativeHostName,
		Description:    "grocery-mcp browser credential relay",
		Path:           binaryPath,
		Type:           "stdio",
		AllowedOrigins: []string{"chrome-extension://" + extensionID + "/"},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		return "", fmt.Errorf("create Native Messaging directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(manifestPath), ".grocery-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("create Native Messaging manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, manifestPath); err != nil {
		return "", fmt.Errorf("install Native Messaging manifest: %w", err)
	}
	return manifestPath, nil
}

func LoadAllowedOrigin(home, goos string) (string, error) {
	manifestPath, err := NativeHostManifestPath(home, goos)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read Native Messaging manifest: %w", err)
	}
	var manifest NativeHostManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("parse Native Messaging manifest: %w", err)
	}
	if manifest.Name != NativeHostName || len(manifest.AllowedOrigins) != 1 {
		return "", fmt.Errorf("Native Messaging manifest is invalid")
	}
	origin := manifest.AllowedOrigins[0]
	if len(origin) != len("chrome-extension://")+32+1 || origin[:len("chrome-extension://")] != "chrome-extension://" || origin[len(origin)-1:] != "/" {
		return "", fmt.Errorf("Native Messaging origin is invalid")
	}
	if !extensionIDPattern.MatchString(origin[len("chrome-extension://") : len(origin)-1]) {
		return "", fmt.Errorf("Native Messaging origin is invalid")
	}
	return origin, nil
}

func NativeHostManifestPath(home, goos string) (string, error) {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "NativeMessagingHosts", NativeHostName+".json"), nil
	case "linux":
		return filepath.Join(home, ".config", "google-chrome", "NativeMessagingHosts", NativeHostName+".json"), nil
	default:
		return "", fmt.Errorf("Native Messaging is unsupported on %s", goos)
	}
}
