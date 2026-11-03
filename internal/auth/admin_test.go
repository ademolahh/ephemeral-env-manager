package auth

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var testSecret = []byte(strings.Repeat("s", MinAdminSecretSize))

func TestIssueAdminToken(t *testing.T) {
	id := uuid.New()
	before := time.Now()

	token, expiresAt, err := IssueAdminToken(testSecret, id, time.Hour)
	if err != nil {
		t.Fatalf("IssueAdminToken() error = %v", err)
	}
	if !IsAdminToken(token) {
		t.Errorf("IsAdminToken(%q) = false, want true", token)
	}
	if want := before.Add(time.Hour); expiresAt.Before(want.Add(-time.Second)) || expiresAt.After(want.Add(time.Second)) {
		t.Errorf("expiresAt = %v, want about %v", expiresAt, want)
	}

	got, err := ParseAdminToken(testSecret, token)
	if err != nil {
		t.Fatalf("ParseAdminToken() error = %v", err)
	}
	if got != id {
		t.Errorf("ParseAdminToken() = %s, want %s", got, id)
	}
}

func TestIsAdminTokenDistinguishesAPIKeys(t *testing.T) {
	key, _ := GenerateAPIKey()
	if IsAdminToken(key) {
		t.Errorf("IsAdminToken(%q) = true for an api key", key)
	}
}

func TestParseAdminTokenRejected(t *testing.T) {
	id := uuid.New()
	expired, _, err := IssueAdminToken(testSecret, id, -time.Hour)
	if err != nil {
		t.Fatalf("IssueAdminToken() expired error = %v", err)
	}
	otherSecret, _, err := IssueAdminToken([]byte(strings.Repeat("o", MinAdminSecretSize)), id, time.Hour)
	if err != nil {
		t.Fatalf("IssueAdminToken() other secret error = %v", err)
	}
	claims := jwt.RegisteredClaims{
		Issuer:    adminTokenIssuer,
		Subject:   id.String(),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	sign := func(method jwt.SigningMethod, key any, c jwt.Claims) string {
		t.Helper()
		s, err := jwt.NewWithClaims(method, c).SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return s
	}
	wrongIssuer := claims
	wrongIssuer.Issuer = "someone-else"
	noExpiry := claims
	noExpiry.ExpiresAt = nil
	badSubject := claims
	badSubject.Subject = "root"

	for name, token := range map[string]string{
		"expired":        expired,
		"other secret":   otherSecret,
		"alg none":       sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims),
		"hs512":          sign(jwt.SigningMethodHS512, testSecret, claims),
		"wrong issuer":   sign(jwt.SigningMethodHS256, testSecret, wrongIssuer),
		"no expiry":      sign(jwt.SigningMethodHS256, testSecret, noExpiry),
		"subject not id": sign(jwt.SigningMethodHS256, testSecret, badSubject),
		"garbage":        "a.b.c",
	} {
		if _, err := ParseAdminToken(testSecret, token); !errors.Is(err, ErrInvalidAdminToken) {
			t.Errorf("%s: ParseAdminToken() error = %v, want %v", name, err, ErrInvalidAdminToken)
		}
	}
}

func TestAdminTokenWeakSecret(t *testing.T) {
	weak := []byte("short")
	if _, _, err := IssueAdminToken(weak, uuid.New(), time.Hour); !errors.Is(err, ErrWeakAdminSecret) {
		t.Errorf("IssueAdminToken() error = %v, want %v", err, ErrWeakAdminSecret)
	}
	if _, err := ParseAdminToken(nil, "a.b.c"); !errors.Is(err, ErrWeakAdminSecret) {
		t.Errorf("ParseAdminToken() error = %v, want %v", err, ErrWeakAdminSecret)
	}
}

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
