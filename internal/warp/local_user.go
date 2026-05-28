package warp

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const warpUserStorageFileName = "dev.warp.Warp-User"

type LocalUserCredential struct {
	RefreshToken string `json:"refresh_token"`
	SourcePath   string `json:"source_path,omitempty"`
}

var decryptLocalUserStorageFunc = decryptLocalUserStorage
var defaultLocalUserStoragePathFunc = defaultLocalUserStoragePath

// SetLocalUserStorageTestHooks is for tests in packages that exercise API
// boundaries around WARP secure storage imports.
func SetLocalUserStorageTestHooks(pathFunc func() (string, error), decryptFunc func([]byte) (string, error)) func() {
	origPathFunc := defaultLocalUserStoragePathFunc
	origDecryptFunc := decryptLocalUserStorageFunc
	if pathFunc != nil {
		defaultLocalUserStoragePathFunc = pathFunc
	}
	if decryptFunc != nil {
		decryptLocalUserStorageFunc = decryptFunc
	}
	return func() {
		defaultLocalUserStoragePathFunc = origPathFunc
		decryptLocalUserStorageFunc = origDecryptFunc
	}
}

// ReadLocalUserCredential extracts id_token.refresh_token from WARP's local
// secure storage without exposing the full persisted User JSON.
func ReadLocalUserCredential() (*LocalUserCredential, error) {
	path, err := defaultLocalUserStoragePathFunc()
	if err != nil {
		return nil, err
	}
	return ReadLocalUserCredentialFromPath(path)
}

func ReadLocalUserCredentialFromPath(path string) (*LocalUserCredential, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("warp local user path is empty")
	}
	encrypted, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read warp local user: %w", err)
	}
	credential, err := ReadLocalUserCredentialFromBytes(encrypted)
	if err != nil {
		return nil, err
	}
	credential.SourcePath = path
	return credential, nil
}

func ReadLocalUserCredentialFromBytes(encrypted []byte) (*LocalUserCredential, error) {
	plaintext, err := decryptLocalUserStorageFunc(encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt warp local user: %w", err)
	}
	token := normalizeRefreshToken(plaintext)
	if token == "" {
		return nil, fmt.Errorf("warp local user missing id_token.refresh_token")
	}
	return &LocalUserCredential{
		RefreshToken: token,
	}, nil
}

func ReadLocalUserCredentialFromReader(r io.Reader, maxBytes int64) (*LocalUserCredential, error) {
	if r == nil {
		return nil, fmt.Errorf("warp local user file is empty")
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read uploaded warp local user: %w", err)
	}
	if n > maxBytes {
		return nil, fmt.Errorf("uploaded warp local user is too large")
	}
	return ReadLocalUserCredentialFromBytes(buf.Bytes())
}

func defaultWindowsLocalUserStoragePath() (string, error) {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if localAppData == "" {
		return "", fmt.Errorf("LOCALAPPDATA is not set")
	}
	return filepath.Join(localAppData, "warp", "Warp", "data", warpUserStorageFileName), nil
}
