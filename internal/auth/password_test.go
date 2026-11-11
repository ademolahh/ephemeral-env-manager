package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if bytes.Contains(hash, []byte("correct horse battery")) {
		t.Error("hash contains the plaintext password")
	}
	if !CheckPassword(hash, "correct horse battery") {
		t.Error("CheckPassword() = false for the right password")
	}
	if CheckPassword(hash, "correct horse battery!") {
		t.Error("CheckPassword() = true for the wrong password")
	}
}

func TestHashPasswordRejected(t *testing.T) {
	for name, password := range map[string]string{
		"too short": "short",
		"too long":  strings.Repeat("a", 73),
	} {
		if _, err := HashPassword(password); err == nil {
			t.Errorf("%s: HashPassword() error = nil, want error", name)
		}
	}
}

func TestCheckPasswordWithoutHash(t *testing.T) {
	if CheckPassword(nil, "anything at all") {
		t.Error("CheckPassword(nil) = true, want false")
	}
}
