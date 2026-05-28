package warp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLocalUserCredentialFromPath_ExtractsNestedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.warp.Warp-User")
	if err := os.WriteFile(path, []byte("encrypted"), 0o600); err != nil {
		t.Fatalf("write temp user: %v", err)
	}

	orig := decryptLocalUserStorageFunc
	decryptLocalUserStorageFunc = func(encrypted []byte) (string, error) {
		if string(encrypted) != "encrypted" {
			t.Fatalf("encrypted=%q want encrypted", encrypted)
		}
		return `{"id_token":{"id_token":"runtime-jwt","refresh_token":"token-123"},"refresh_token":""}`, nil
	}
	t.Cleanup(func() {
		decryptLocalUserStorageFunc = orig
	})

	credential, err := ReadLocalUserCredentialFromPath(path)
	if err != nil {
		t.Fatalf("ReadLocalUserCredentialFromPath() error: %v", err)
	}
	if credential.RefreshToken != "token-123" {
		t.Fatalf("RefreshToken=%q want token-123", credential.RefreshToken)
	}
	if credential.SourcePath != path {
		t.Fatalf("SourcePath=%q want %q", credential.SourcePath, path)
	}
}

func TestReadLocalUserCredential_RealStorage(t *testing.T) {
	if os.Getenv("WARP_LOCAL_USER_REAL") != "1" {
		t.Skip("set WARP_LOCAL_USER_REAL=1 to verify the current Windows user's WARP secure storage")
	}

	credential, err := ReadLocalUserCredential()
	if err != nil {
		t.Fatalf("ReadLocalUserCredential() error: %v", err)
	}
	if strings.TrimSpace(credential.RefreshToken) == "" {
		t.Fatal("RefreshToken is empty")
	}
	if strings.TrimSpace(credential.SourcePath) == "" {
		t.Fatal("SourcePath is empty")
	}
}
