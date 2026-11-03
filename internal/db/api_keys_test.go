package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type apiKeyRow struct {
	Hash      []byte
	CallerID  uuid.UUID
	ExpiresAt time.Time
	Valid     bool
}

func getAPIKey(t *testing.T, tx pgx.Tx, id uuid.UUID) apiKeyRow {
	t.Helper()
	var r apiKeyRow
	err := tx.QueryRow(context.Background(), `
		SELECT api_key_hash, caller_id, expires_at, valid
		FROM api_keys WHERE id = $1`, id,
	).Scan(&r.Hash, &r.CallerID, &r.ExpiresAt, &r.Valid)
	if err != nil {
		t.Fatalf("get api key %s: %v", id, err)
	}
	return r
}

func txNow(t *testing.T, tx pgx.Tx) time.Time {
	t.Helper()
	var now time.Time
	if err := tx.QueryRow(context.Background(), `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("select now: %v", err)
	}
	return now
}

func createTestCaller(t *testing.T, tx pgx.Tx, c NewCaller) uuid.UUID {
	t.Helper()
	id, err := CreateCaller(context.Background(), tx, c)
	if err != nil {
		t.Fatalf("CreateCaller() error = %v", err)
	}
	return id
}

func createTestAPIKey(t *testing.T, tx pgx.Tx, callerID uuid.UUID, hash []byte, expiresAt time.Time) uuid.UUID {
	t.Helper()
	id, err := CreateAPIKey(context.Background(), tx, callerID, hash, expiresAt)
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	return id
}

func TestCreateAPIKey(t *testing.T) {
	tx := testTx(t)
	callerID := createTestCaller(t, tx, newCaller())
	hash := []byte{0x00, 0x01, 0xfe, 0xff}
	expires := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	id, err := CreateAPIKey(context.Background(), tx, callerID, hash, expires)
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	if id == uuid.Nil {
		t.Fatal("CreateAPIKey() returned nil id")
	}

	got := getAPIKey(t, tx, id)
	if string(got.Hash) != string(hash) {
		t.Errorf("api_key_hash = %x, want %x", got.Hash, hash)
	}
	if got.CallerID != callerID {
		t.Errorf("caller_id = %s, want %s", got.CallerID, callerID)
	}
	if !got.ExpiresAt.Equal(expires) {
		t.Errorf("expires_at = %v, want %v", got.ExpiresAt, expires)
	}
	if !got.Valid {
		t.Error("valid = false, want true")
	}
}

func TestCreateAPIKeyErrors(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, tx pgx.Tx, callerID uuid.UUID)
		caller func(callerID uuid.UUID) uuid.UUID
		hash   []byte
		code   string
	}{
		{
			name:   "unknown caller",
			caller: func(uuid.UUID) uuid.UUID { return uuid.New() },
			hash:   []byte("hash"),
			code:   "23503",
		},
		{
			name: "duplicate hash",
			setup: func(t *testing.T, tx pgx.Tx, callerID uuid.UUID) {
				createTestAPIKey(t, tx, callerID, []byte("hash"), time.Now().Add(time.Hour))
			},
			caller: func(callerID uuid.UUID) uuid.UUID { return callerID },
			hash:   []byte("hash"),
			code:   "23505",
		},
		{
			name:   "nil hash",
			caller: func(callerID uuid.UUID) uuid.UUID { return callerID },
			hash:   nil,
			code:   "23502",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			callerID := createTestCaller(t, tx, newCaller())
			if tt.setup != nil {
				tt.setup(t, tx, callerID)
			}

			id, err := CreateAPIKey(context.Background(), tx, tt.caller(callerID), tt.hash, time.Now().Add(time.Hour))
			if err == nil {
				t.Fatal("CreateAPIKey() error = nil, want error")
			}
			if id != uuid.Nil {
				t.Errorf("CreateAPIKey() id = %s, want nil", id)
			}
			if !strings.HasPrefix(err.Error(), "create api key:") {
				t.Errorf("CreateAPIKey() error = %v, want create api key prefix", err)
			}
			if got := pgCode(err); got != tt.code {
				t.Errorf("CreateAPIKey() error code = %q, want %q", got, tt.code)
			}
		})
	}
}

func TestCallerByAPIKey(t *testing.T) {
	tx := testTx(t)
	c := newCaller()
	c.Role = RoleAgent
	c.PermittedEnv = EnvProdBug
	callerID := createTestCaller(t, tx, c)
	hash := []byte("secret-hash")
	createTestAPIKey(t, tx, callerID, hash, time.Now().Add(time.Hour))

	got, err := CallerByAPIKey(context.Background(), tx, hash)
	if err != nil {
		t.Fatalf("CallerByAPIKey() error = %v", err)
	}
	want := AuthenticatedCaller{ID: callerID, Role: RoleAgent, PermittedEnv: EnvProdBug}
	if got != want {
		t.Errorf("CallerByAPIKey() = %+v, want %+v", got, want)
	}
}

func TestCallerByAPIKeyMultipleCallers(t *testing.T) {
	tx := testTx(t)
	alice := newCaller()
	alice.GithubID = new("1")
	bob := newCaller()
	bob.GithubID = new("2")
	bob.Role = RoleAgent
	aliceID := createTestCaller(t, tx, alice)
	bobID := createTestCaller(t, tx, bob)
	expires := time.Now().Add(time.Hour)

	createTestAPIKey(t, tx, aliceID, []byte("alice-1"), expires)
	createTestAPIKey(t, tx, aliceID, []byte("alice-2"), expires)
	createTestAPIKey(t, tx, bobID, []byte("bob-1"), expires)

	tests := []struct {
		hash string
		want uuid.UUID
		role Role
	}{
		{hash: "alice-1", want: aliceID, role: alice.Role},
		{hash: "alice-2", want: aliceID, role: alice.Role},
		{hash: "bob-1", want: bobID, role: bob.Role},
	}

	for _, tt := range tests {
		t.Run(tt.hash, func(t *testing.T) {
			got, err := CallerByAPIKey(context.Background(), tx, []byte(tt.hash))
			if err != nil {
				t.Fatalf("CallerByAPIKey() error = %v", err)
			}
			if got.ID != tt.want || got.Role != tt.role {
				t.Errorf("CallerByAPIKey() = %+v, want id %s role %s", got, tt.want, tt.role)
			}
		})
	}
}

func TestCallerByAPIKeyNotFound(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, tx pgx.Tx, callerID uuid.UUID, hash []byte)
	}{
		{
			name:  "unknown hash",
			setup: func(*testing.T, pgx.Tx, uuid.UUID, []byte) {},
		},
		{
			name: "expired",
			setup: func(t *testing.T, tx pgx.Tx, callerID uuid.UUID, hash []byte) {
				createTestAPIKey(t, tx, callerID, hash, txNow(t, tx).Add(-time.Second))
			},
		},
		{
			name: "expires now",
			setup: func(t *testing.T, tx pgx.Tx, callerID uuid.UUID, hash []byte) {
				createTestAPIKey(t, tx, callerID, hash, txNow(t, tx))
			},
		},
		{
			name: "revoked",
			setup: func(t *testing.T, tx pgx.Tx, callerID uuid.UUID, hash []byte) {
				id := createTestAPIKey(t, tx, callerID, hash, time.Now().Add(time.Hour))
				if err := RevokeAPIKey(context.Background(), tx, id); err != nil {
					t.Fatalf("RevokeAPIKey() error = %v", err)
				}
			},
		},
		{
			name: "different hash",
			setup: func(t *testing.T, tx pgx.Tx, callerID uuid.UUID, hash []byte) {
				createTestAPIKey(t, tx, callerID, append([]byte("x"), hash...), time.Now().Add(time.Hour))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			callerID := createTestCaller(t, tx, newCaller())
			hash := []byte("secret-hash")
			tt.setup(t, tx, callerID, hash)

			got, err := CallerByAPIKey(context.Background(), tx, hash)
			if !errors.Is(err, ErrAPIKeyNotFound) {
				t.Errorf("CallerByAPIKey() error = %v, want ErrAPIKeyNotFound", err)
			}
			if got != (AuthenticatedCaller{}) {
				t.Errorf("CallerByAPIKey() = %+v, want zero value", got)
			}
		})
	}
}

func TestCallerByAPIKeyQueryError(t *testing.T) {
	tx := testTx(t)
	if _, err := tx.Exec(context.Background(), `DROP TABLE api_keys`); err != nil {
		t.Fatalf("drop api_keys: %v", err)
	}

	_, err := CallerByAPIKey(context.Background(), tx, []byte("hash"))
	if errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("CallerByAPIKey() error = %v, want query error", err)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "caller by api key:") {
		t.Errorf("CallerByAPIKey() error = %v, want caller by api key prefix", err)
	}
}

func TestRevokeAPIKey(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	expires := time.Now().Add(time.Hour)
	revoked := createTestAPIKey(t, tx, callerID, []byte("revoked"), expires)
	kept := createTestAPIKey(t, tx, callerID, []byte("kept"), expires)

	if err := RevokeAPIKey(ctx, tx, revoked); err != nil {
		t.Fatalf("RevokeAPIKey() error = %v", err)
	}

	if getAPIKey(t, tx, revoked).Valid {
		t.Error("revoked key valid = true, want false")
	}
	if !getAPIKey(t, tx, kept).Valid {
		t.Error("other key valid = false, want true")
	}
	if _, err := CallerByAPIKey(ctx, tx, []byte("kept")); err != nil {
		t.Errorf("CallerByAPIKey() for other key error = %v", err)
	}
}

func TestRevokeAPIKeyTwice(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	id := createTestAPIKey(t, tx, callerID, []byte("hash"), time.Now().Add(time.Hour))

	for i := range 2 {
		if err := RevokeAPIKey(ctx, tx, id); err != nil {
			t.Fatalf("RevokeAPIKey() call %d error = %v", i+1, err)
		}
	}
	if getAPIKey(t, tx, id).Valid {
		t.Error("valid = true, want false")
	}
}

func TestRevokeAPIKeyNotFound(t *testing.T) {
	tx := testTx(t)
	err := RevokeAPIKey(context.Background(), tx, uuid.New())
	if !errors.Is(err, ErrAPIKeyNotFound) {
		t.Errorf("RevokeAPIKey() error = %v, want ErrAPIKeyNotFound", err)
	}
}

func TestRevokeAPIKeyQueryError(t *testing.T) {
	tx := testTx(t)
	if _, err := tx.Exec(context.Background(), `DROP TABLE api_keys`); err != nil {
		t.Fatalf("drop api_keys: %v", err)
	}

	id := uuid.New()
	err := RevokeAPIKey(context.Background(), tx, id)
	if errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("RevokeAPIKey() error = %v, want query error", err)
	}
	if want := "revoke api key " + id.String() + ":"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("RevokeAPIKey() error = %v, want %q prefix", err, want)
	}
}
