package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestGenerateAPIKey(t *testing.T) {
	key, hash := GenerateAPIKey()

	if len(key) != 43 {
		t.Errorf("len(key) = %d, want 43", len(key))
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("decode key %q: %v", key, err)
	}
	if len(raw) != 32 {
		t.Errorf("decoded key length = %d, want 32", len(raw))
	}
	if !bytes.Equal(hash, HashToken(key)) {
		t.Errorf("GenerateAPIKey() hash = %x, want HashToken(key) = %x", hash, HashToken(key))
	}
}

func TestGenerateAPIKeyUnique(t *testing.T) {
	const n = 1000
	keys := make(map[string]bool, n)
	hashes := make(map[string]bool, n)
	for range n {
		key, hash := GenerateAPIKey()
		if keys[key] {
			t.Fatalf("GenerateAPIKey() returned duplicate key %q", key)
		}
		if hashes[string(hash)] {
			t.Fatalf("GenerateAPIKey() returned duplicate hash %x", hash)
		}
		keys[key] = true
		hashes[string(hash)] = true
	}
}

func TestHashToken(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "empty",
			key:  "",
			want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name: "key",
			key:  "eem_test",
			want: "350e60f38edb69bdd9f829efbf9cad6acbef8988a29275a92367711a6bac449d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hex.EncodeToString(HashToken(tt.key)); got != tt.want {
				t.Errorf("HashToken(%q) = %s, want %s", tt.key, got, tt.want)
			}
		})
	}
}

func TestHashTokenDistinct(t *testing.T) {
	a := HashToken("eem_a")
	b := HashToken("eem_b")
	if bytes.Equal(a, b) {
		t.Errorf("HashToken() returned same hash %x for different keys", a)
	}
	if !bytes.Equal(a, HashToken("eem_a")) {
		t.Error("HashToken() not deterministic")
	}
}
