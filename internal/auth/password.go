package auth

import (
	"fmt"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	MinPasswordLength = 12
	maxPasswordBytes  = 72
)

var dummyPasswordHash = sync.OnceValue(func() []byte {
	hash, _ := bcrypt.GenerateFromPassword([]byte("not a real password"), bcrypt.DefaultCost)
	return hash
})

func HashPassword(password string) ([]byte, error) {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return nil, fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}

	if len(password) > maxPasswordBytes {
		return nil, fmt.Errorf("password must be at most %d bytes", maxPasswordBytes)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	return hash, nil
}

func CheckPassword(hash []byte, password string) bool {
	if hash == nil {
		bcrypt.CompareHashAndPassword(dummyPasswordHash(), []byte(password))
		return false
	}

	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}
