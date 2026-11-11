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
	Hash       []byte
	CallerID   uuid.UUID
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

func getAPIKey(t *testing.T, tx pgx.Tx, id uuid.UUID) apiKeyRow {
	t.Helper()
	var r apiKeyRow
	err := tx.QueryRow(context.Background(), `
		SELECT api_key_hash, caller_id, expires_at, last_used_at, revoked_at
		FROM api_keys WHERE id = $1`, id,
	).Scan(&r.Hash, &r.CallerID, &r.ExpiresAt, &r.LastUsedAt, &r.RevokedAt)
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

func newAPIKey(callerID uuid.UUID, hash []byte, expiresAt time.Time) NewAPIKey {
	return NewAPIKey{CallerID: callerID, Name: "test", Hash: hash, ExpiresAt: expiresAt}
}

func createTestAPIKey(t *testing.T, tx pgx.Tx, k NewAPIKey) uuid.UUID {
	t.Helper()
	created, err := CreateAPIKey(context.Background(), tx, k)
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	return created.ID
}

func TestCreateAPIKey(t *testing.T) {
	tx := testTx(t)
	callerID := createTestCaller(t, tx, newCaller())
	hash := []byte{0x00, 0x01, 0xfe, 0xff}
	expires := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	review := EnvPRReview
	costCap := int64(500)

	got, err := CreateAPIKey(context.Background(), tx, NewAPIKey{
		CallerID:     callerID,
		Name:         "ci agent",
		Hash:         hash,
		Role:         RoleAgent,
		PermittedEnv: &review,
		CostCap:      &costCap,
		ExpiresAt:    expires,
	})
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	if got.ID == uuid.Nil || got.CallerID != callerID || got.Name != "ci agent" || got.Role != RoleAgent {
		t.Errorf("CreateAPIKey() = %+v", got)
	}
	if got.PermittedEnv == nil || *got.PermittedEnv != review || got.CostCap == nil || *got.CostCap != costCap {
		t.Errorf("permitted_env, cost_cap = %v, %v, want %s, %d", got.PermittedEnv, got.CostCap, review, costCap)
	}
	if !got.ExpiresAt.Equal(expires) || got.CostSpent != 0 || got.LastUsedAt != nil || got.RevokedAt != nil {
		t.Errorf("CreateAPIKey() = %+v, want fresh key expiring %v", got, expires)
	}

	row := getAPIKey(t, tx, got.ID)
	if string(row.Hash) != string(hash) {
		t.Errorf("api_key_hash = %x, want %x", row.Hash, hash)
	}
}

func TestCreateAPIKeyDefaultsToUser(t *testing.T) {
	tx := testTx(t)
	callerID := createTestCaller(t, tx, newCaller())
	got, err := CreateAPIKey(context.Background(), tx, newAPIKey(callerID, []byte("h"), time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	if got.Role != RoleUser || got.PermittedEnv != nil || got.CostCap != nil {
		t.Errorf("CreateAPIKey() = %+v, want user key without limits", got)
	}
}

func TestCreateAPIKeyErrors(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, tx pgx.Tx, callerID uuid.UUID)
		mutate func(k *NewAPIKey)
		code   string
	}{
		{
			name:   "unknown caller",
			mutate: func(k *NewAPIKey) { k.CallerID = uuid.New() },
			code:   "23503",
		},
		{
			name: "duplicate hash",
			setup: func(t *testing.T, tx pgx.Tx, callerID uuid.UUID) {
				createTestAPIKey(t, tx, newAPIKey(callerID, []byte("hash"), time.Now().Add(time.Hour)))
			},
			code: "23505",
		},
		{
			name:   "nil hash",
			mutate: func(k *NewAPIKey) { k.Hash = nil },
			code:   "23502",
		},
		{
			name:   "empty name",
			mutate: func(k *NewAPIKey) { k.Name = "" },
			code:   "23514",
		},
		{
			name:   "negative cost cap",
			mutate: func(k *NewAPIKey) { k.CostCap = new(int64(-1)) },
			code:   "23514",
		},
		{
			name:   "unknown role",
			mutate: func(k *NewAPIKey) { k.Role = "admin" },
			code:   "22P02",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			callerID := createTestCaller(t, tx, newCaller())
			if tt.setup != nil {
				tt.setup(t, tx, callerID)
			}

			k := newAPIKey(callerID, []byte("hash"), time.Now().Add(time.Hour))
			if tt.mutate != nil {
				tt.mutate(&k)
			}
			_, err := CreateAPIKey(context.Background(), tx, k)
			if err == nil {
				t.Fatal("CreateAPIKey() error = nil, want error")
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
	c.PermittedEnv = EnvProdBug
	callerID := createTestCaller(t, tx, c)
	hash := []byte("secret-hash")
	k := newAPIKey(callerID, hash, time.Now().Add(time.Hour))
	k.Role = RoleAgent
	keyID := createTestAPIKey(t, tx, k)

	got, err := CallerByAPIKey(context.Background(), tx, hash)
	if err != nil {
		t.Fatalf("CallerByAPIKey() error = %v", err)
	}
	want := AuthenticatedCaller{ID: callerID, Role: RoleAgent, PermittedEnv: EnvProdBug, KeyID: keyID}
	if got != want {
		t.Errorf("CallerByAPIKey() = %+v, want %+v", got, want)
	}
	if got.AgentKey() == nil || *got.AgentKey() != keyID {
		t.Errorf("AgentKey() = %v, want %s", got.AgentKey(), keyID)
	}
}

func TestCallerByAPIKeyPermission(t *testing.T) {
	review, prodBug := EnvPRReview, EnvProdBug
	tests := []struct {
		name   string
		caller EnvType
		key    *EnvType
		want   EnvType
	}{
		{"inherits caller", EnvPRReview, nil, EnvPRReview},
		{"key lowers", EnvProdBug, &review, EnvPRReview},
		{"key cannot raise", EnvPRReview, &prodBug, EnvPRReview},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			c := newCaller()
			c.PermittedEnv = tt.caller
			callerID := createTestCaller(t, tx, c)
			k := newAPIKey(callerID, []byte("hash"), time.Now().Add(time.Hour))
			k.PermittedEnv = tt.key
			createTestAPIKey(t, tx, k)

			got, err := CallerByAPIKey(context.Background(), tx, []byte("hash"))
			if err != nil {
				t.Fatalf("CallerByAPIKey() error = %v", err)
			}
			if got.PermittedEnv != tt.want {
				t.Errorf("permitted_env = %s, want %s", got.PermittedEnv, tt.want)
			}
		})
	}
}

func TestCallerByAPIKeyTouchesLastUsed(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	id := createTestAPIKey(t, tx, newAPIKey(callerID, []byte("hash"), time.Now().Add(time.Hour)))

	if _, err := CallerByAPIKey(ctx, tx, []byte("hash")); err != nil {
		t.Fatalf("CallerByAPIKey() error = %v", err)
	}
	used := getAPIKey(t, tx, id).LastUsedAt
	if used == nil || !used.Equal(txNow(t, tx)) {
		t.Fatalf("last_used_at = %v, want now", used)
	}

	if _, err := tx.Exec(ctx, `UPDATE api_keys SET last_used_at = now() - interval '30 seconds' WHERE id = $1`, id); err != nil {
		t.Fatalf("age last_used_at: %v", err)
	}
	recent := getAPIKey(t, tx, id).LastUsedAt
	if _, err := CallerByAPIKey(ctx, tx, []byte("hash")); err != nil {
		t.Fatalf("CallerByAPIKey() error = %v", err)
	}
	if got := getAPIKey(t, tx, id).LastUsedAt; !got.Equal(*recent) {
		t.Errorf("last_used_at = %v, want unchanged %v within a minute", got, recent)
	}
}

func TestCallerByAPIKeyMultipleCallers(t *testing.T) {
	tx := testTx(t)
	alice := newCaller()
	alice.GithubID = new("1")
	bob := newCaller()
	bob.GithubID = new("2")
	aliceID := createTestCaller(t, tx, alice)
	bobID := createTestCaller(t, tx, bob)
	expires := time.Now().Add(time.Hour)

	createTestAPIKey(t, tx, newAPIKey(aliceID, []byte("alice-1"), expires))
	createTestAPIKey(t, tx, newAPIKey(aliceID, []byte("alice-2"), expires))
	agentKey := newAPIKey(bobID, []byte("bob-1"), expires)
	agentKey.Role = RoleAgent
	createTestAPIKey(t, tx, agentKey)

	tests := []struct {
		hash string
		want uuid.UUID
		role Role
	}{
		{hash: "alice-1", want: aliceID, role: RoleUser},
		{hash: "alice-2", want: aliceID, role: RoleUser},
		{hash: "bob-1", want: bobID, role: RoleAgent},
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
				createTestAPIKey(t, tx, newAPIKey(callerID, hash, txNow(t, tx).Add(-time.Second)))
			},
		},
		{
			name: "expires now",
			setup: func(t *testing.T, tx pgx.Tx, callerID uuid.UUID, hash []byte) {
				createTestAPIKey(t, tx, newAPIKey(callerID, hash, txNow(t, tx)))
			},
		},
		{
			name: "revoked",
			setup: func(t *testing.T, tx pgx.Tx, callerID uuid.UUID, hash []byte) {
				id := createTestAPIKey(t, tx, newAPIKey(callerID, hash, time.Now().Add(time.Hour)))
				if err := RevokeAPIKey(context.Background(), tx, id, callerID); err != nil {
					t.Fatalf("RevokeAPIKey() error = %v", err)
				}
			},
		},
		{
			name: "different hash",
			setup: func(t *testing.T, tx pgx.Tx, callerID uuid.UUID, hash []byte) {
				createTestAPIKey(t, tx, newAPIKey(callerID, append([]byte("x"), hash...), time.Now().Add(time.Hour)))
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
	if _, err := tx.Exec(context.Background(), `DROP TABLE api_keys CASCADE`); err != nil {
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

func TestListAPIKeys(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	other := newCaller()
	other.GithubID = new("other")
	otherID := createTestCaller(t, tx, other)
	expires := time.Now().Add(time.Hour)

	first := createTestAPIKey(t, tx, newAPIKey(callerID, []byte("a"), expires))
	second := createTestAPIKey(t, tx, newAPIKey(callerID, []byte("b"), expires))
	createTestAPIKey(t, tx, newAPIKey(otherID, []byte("c"), expires))
	if _, err := tx.Exec(ctx, `UPDATE api_keys SET created_at = now() - interval '1 hour' WHERE id = $1`, first); err != nil {
		t.Fatalf("age key: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO environments (id, expires_at, type, provider, cpu_cores, ram_gb, disk_gb, hourly_rate,
			caller_id, api_key_id, template_name, template_version, estimated_cost, reserved_cost, charged_cost)
		VALUES
			(gen_random_uuid(), now(), 'PR_REVIEW', 'docker', 1, 1, 1, 1, $1, $2, 't', 'v1', 100, 100, NULL),
			(gen_random_uuid(), now(), 'PR_REVIEW', 'docker', 1, 1, 1, 1, $1, $2, 't', 'v1', 100, 100, 40)`,
		callerID, second); err != nil {
		t.Fatalf("insert environments: %v", err)
	}

	keys, err := ListAPIKeys(ctx, tx, callerID)
	if err != nil {
		t.Fatalf("ListAPIKeys() error = %v", err)
	}
	if len(keys) != 2 || keys[0].ID != second || keys[1].ID != first {
		t.Fatalf("ListAPIKeys() = %+v, want [%s %s]", keys, second, first)
	}
	if keys[0].CostSpent != 140 || keys[1].CostSpent != 0 {
		t.Errorf("cost spent = %d, %d, want 140, 0", keys[0].CostSpent, keys[1].CostSpent)
	}
}

func TestLockAPIKey(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	k := newAPIKey(callerID, []byte("hash"), time.Now().Add(time.Hour))
	k.Role = RoleAgent
	k.CostCap = new(int64(10))
	id := createTestAPIKey(t, tx, k)

	got, err := LockAPIKey(ctx, tx, callerID, id)
	if err != nil {
		t.Fatalf("LockAPIKey() error = %v", err)
	}
	if got.Role != RoleAgent || got.CostCap == nil || *got.CostCap != 10 || got.CostSpent != 0 {
		t.Errorf("LockAPIKey() = %+v", got)
	}

	if _, err := LockAPIKey(ctx, tx, uuid.New(), id); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Errorf("LockAPIKey() other caller error = %v, want ErrAPIKeyNotFound", err)
	}

	if err := RevokeAPIKey(ctx, tx, id, callerID); err != nil {
		t.Fatalf("RevokeAPIKey() error = %v", err)
	}
	if _, err := LockAPIKey(ctx, tx, callerID, id); !errors.Is(err, ErrAPIKeyInactive) {
		t.Errorf("LockAPIKey() revoked error = %v, want ErrAPIKeyInactive", err)
	}
}

func TestRevokeAPIKey(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	expires := time.Now().Add(time.Hour)
	revoked := createTestAPIKey(t, tx, newAPIKey(callerID, []byte("revoked"), expires))
	kept := createTestAPIKey(t, tx, newAPIKey(callerID, []byte("kept"), expires))

	if err := RevokeAPIKey(ctx, tx, revoked, callerID); err != nil {
		t.Fatalf("RevokeAPIKey() error = %v", err)
	}

	if getAPIKey(t, tx, revoked).RevokedAt == nil {
		t.Error("revoked_at = nil, want set")
	}
	if getAPIKey(t, tx, kept).RevokedAt != nil {
		t.Error("other key revoked_at set, want nil")
	}
	if _, err := CallerByAPIKey(ctx, tx, []byte("kept")); err != nil {
		t.Errorf("CallerByAPIKey() for other key error = %v", err)
	}
}

func TestRevokeAPIKeyTwiceKeepsTime(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	id := createTestAPIKey(t, tx, newAPIKey(callerID, []byte("hash"), time.Now().Add(time.Hour)))

	if err := RevokeAPIKey(ctx, tx, id, callerID); err != nil {
		t.Fatalf("RevokeAPIKey() error = %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at = revoked_at - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatalf("age revoked_at: %v", err)
	}
	first := getAPIKey(t, tx, id).RevokedAt
	if err := RevokeAPIKey(ctx, tx, id, callerID); err != nil {
		t.Fatalf("RevokeAPIKey() again error = %v", err)
	}
	if got := getAPIKey(t, tx, id).RevokedAt; !got.Equal(*first) {
		t.Errorf("revoked_at = %v, want unchanged %v", got, first)
	}
}

func TestRevokeAPIKeyOwnership(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	id := createTestAPIKey(t, tx, newAPIKey(callerID, []byte("hash"), time.Now().Add(time.Hour)))

	if err := RevokeAPIKey(ctx, tx, id, uuid.New()); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Errorf("RevokeAPIKey() by other caller error = %v, want ErrAPIKeyNotFound", err)
	}
	if getAPIKey(t, tx, id).RevokedAt != nil {
		t.Error("revoked_at set by other caller")
	}
	if err := RevokeAPIKey(ctx, tx, id, uuid.Nil); err != nil {
		t.Errorf("RevokeAPIKey() as admin error = %v", err)
	}
}

func TestRevokeAPIKeyNotFound(t *testing.T) {
	tx := testTx(t)
	err := RevokeAPIKey(context.Background(), tx, uuid.New(), uuid.Nil)
	if !errors.Is(err, ErrAPIKeyNotFound) {
		t.Errorf("RevokeAPIKey() error = %v, want ErrAPIKeyNotFound", err)
	}
}

func TestRevokeAPIKeyQueryError(t *testing.T) {
	tx := testTx(t)
	if _, err := tx.Exec(context.Background(), `DROP TABLE api_keys CASCADE`); err != nil {
		t.Fatalf("drop api_keys: %v", err)
	}

	id := uuid.New()
	err := RevokeAPIKey(context.Background(), tx, id, uuid.Nil)
	if errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("RevokeAPIKey() error = %v, want query error", err)
	}
	if want := "revoke api key " + id.String() + ":"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("RevokeAPIKey() error = %v, want %q prefix", err, want)
	}
}
