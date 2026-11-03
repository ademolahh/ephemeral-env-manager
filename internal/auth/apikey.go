package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

func GenerateAPIKey() (key string, hash []byte) {
	key = generateToken("eem_")
	return key, HashAPIKey(key)
}

func HashAPIKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

func generateToken(prefix string) string {
	b := make([]byte, 32)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}
