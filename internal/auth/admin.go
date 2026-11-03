package auth

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	MinPasswordLength  = 12
	MinAdminSecretSize = 32
	maxPasswordBytes   = 72
	adminTokenIssuer   = "eem"
)

var (
	ErrInvalidAdminToken = errors.New("invalid or expired admin token")
	ErrWeakAdminSecret   = fmt.Errorf("admin token secret must be at least %d bytes", MinAdminSecretSize)
)

var dummyPasswordHash = sync.OnceValue(func() []byte {
	hash, _ := bcrypt.GenerateFromPassword([]byte("not a real password"), bcrypt.DefaultCost)
	return hash
})

func IsAdminToken(token string) bool {
	return strings.Count(token, ".") == 2
}

func IssueAdminToken(secret []byte, adminID uuid.UUID, ttl time.Duration) (string, time.Time, error) {
	if len(secret) < MinAdminSecretSize {
		return "", time.Time{}, ErrWeakAdminSecret
	}

	now := time.Now()
	expiresAt := now.Add(ttl).UTC().Truncate(time.Second)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    adminTokenIssuer,
		Subject:   adminID.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	}).SignedString(secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign admin token: %w", err)
	}
	return token, expiresAt, nil
}

func ParseAdminToken(secret []byte, token string) (uuid.UUID, error) {
	if len(secret) < MinAdminSecretSize {
		return uuid.Nil, ErrWeakAdminSecret
	}

	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(adminTokenIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrInvalidAdminToken, err)
	}

	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: subject %q is not an admin id", ErrInvalidAdminToken, claims.Subject)
	}
	return id, nil
}

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
