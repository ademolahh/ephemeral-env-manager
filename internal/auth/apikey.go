package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

func GenerateAPIKey() (key string, hash []byte) {
	b := make([]byte, 32)
	rand.Read(b)
	key = "eem_" + base64.RawURLEncoding.EncodeToString(b)
	return key, HashAPIKey(key)
}

func HashAPIKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}
