package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/ademolahh/ephemeral-env-manager/internal/provider"
)

func newEnvironment(callerID uuid.UUID) NewEnvironment {
	return NewEnvironment{
		CallerID:        callerID,
		Type:            EnvPRReview,
		Provider:        provider.Docker,
		Resources:       provider.Resources{CPUCore: 3, RAMGB: 9, DiskGB: 22},
		HourlyRate:      decimal.RequireFromString("0.054569"),
		TTL:             4 * time.Hour,
		TemplateName:    "clear-dashboard",
		TemplateVersion: "v1",
	}
}

func createTestEnvironment(t *testing.T, tx pgx.Tx, e NewEnvironment) uuid.UUID {
	t.Helper()
	id, err := CreateEnvironment(context.Background(), tx, e)
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	return id
}

func forceStatus(t *testing.T, tx pgx.Tx, id uuid.UUID, status EnvStatus) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), `UPDATE environments SET status = $2::env_status WHERE id = $1`, id, status); err != nil {
		t.Fatalf("force status %s: %v", status, err)
	}
}

func envIDs(envs []Environment) []uuid.UUID {
	ids := make([]uuid.UUID, len(envs))
	for i, e := range envs {
		ids[i] = e.ID
	}
	return ids
}

func TestCreateEnvironment(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	want := newEnvironment(callerID)

	id, err := CreateEnvironment(ctx, tx, want)
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	if id == uuid.Nil {
		t.Fatal("CreateEnvironment() returned nil id")
	}

	got, err := GetEnvironment(ctx, tx, id)
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if got.ID != id {
		t.Errorf("id = %s, want %s", got.ID, id)
	}
	if got.Status != EnvProvisioning {
		t.Errorf("status = %s, want %s", got.Status, EnvProvisioning)
	}
	if got.Type != want.Type {
		t.Errorf("type = %s, want %s", got.Type, want.Type)
	}
	if got.Provider != want.Provider {
		t.Errorf("provider = %s, want %s", got.Provider, want.Provider)
	}
	if got.Resources != want.Resources {
		t.Errorf("resources = %+v, want %+v", got.Resources, want.Resources)
	}
	if !got.HourlyRate.Equal(want.HourlyRate) {
		t.Errorf("hourly_rate = %s, want %s", got.HourlyRate, want.HourlyRate)
	}
	if got.CallerID != callerID {
		t.Errorf("caller_id = %s, want %s", got.CallerID, callerID)
	}
	if got.TemplateName != want.TemplateName || got.TemplateVersion != want.TemplateVersion {
		t.Errorf("template = %s@%s, want %s@%s", got.TemplateName, got.TemplateVersion, want.TemplateName, want.TemplateVersion)
	}
	if ttl := got.ExpiresAt.Sub(got.CreatedAt); ttl != want.TTL {
		t.Errorf("expires_at - created_at = %s, want %s", ttl, want.TTL)
	}
	if got.DeletedAt != nil || got.Endpoint != nil || got.FailureReason != nil {
		t.Errorf("deleted_at, endpoint, failure_reason = %v, %v, %v, want all nil", got.DeletedAt, got.Endpoint, got.FailureReason)
	}
}

func TestCreateEnvironmentErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NewEnvironment)
		code   string
	}{
		{name: "unknown caller", mutate: func(e *NewEnvironment) { e.CallerID = uuid.New() }, code: "23503"},
		{name: "invalid type", mutate: func(e *NewEnvironment) { e.Type = "NOPE" }, code: "22P02"},
		{name: "invalid provider", mutate: func(e *NewEnvironment) { e.Provider = "aws" }, code: "22P02"},
		{name: "negative resources", mutate: func(e *NewEnvironment) { e.Resources.RAMGB = -1 }, code: "23514"},
		{name: "negative hourly rate", mutate: func(e *NewEnvironment) { e.HourlyRate = decimal.NewFromInt(-1) }, code: "23514"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := testTx(t)
			e := newEnvironment(createTestCaller(t, tx, newCaller()))
			tt.mutate(&e)
			_, err := CreateEnvironment(context.Background(), tx, e)
			if got := pgCode(err); got != tt.code {
				t.Errorf("CreateEnvironment() error = %v, want code %s", err, tt.code)
			}
		})
	}
}

func TestGetEnvironmentNotFound(t *testing.T) {
	tx := testTx(t)
	_, err := GetEnvironment(context.Background(), tx, uuid.New())
	if !errors.Is(err, ErrEnvNotFound) {
		t.Errorf("GetEnvironment() error = %v, want %v", err, ErrEnvNotFound)
	}
}

func TestEnvironmentTransitions(t *testing.T) {
	mark := map[EnvStatus]func(context.Context, DBTX, uuid.UUID) error{
		EnvRunning:  MarkEnvironmentRunning,
		EnvDeleting: MarkEnvironmentDeleting,
		EnvDeleted:  MarkEnvironmentDeleted,
		EnvFailed: func(ctx context.Context, q DBTX, id uuid.UUID) error {
			return MarkEnvironmentFailed(ctx, q, id, "boom")
		},
	}
	all := []EnvStatus{EnvProvisioning, EnvRunning, EnvDeleting, EnvDeleted, EnvFailed}
	allowed := map[EnvStatus][]EnvStatus{
		EnvProvisioning: {EnvRunning, EnvFailed, EnvDeleting},
		EnvRunning:      {EnvDeleting, EnvFailed},
		EnvDeleting:     {EnvDeleted},
	}

	for _, from := range all {
		for _, to := range []EnvStatus{EnvRunning, EnvDeleting, EnvDeleted, EnvFailed} {
			ok := false
			for _, s := range allowed[from] {
				ok = ok || s == to
			}
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				tx := testTx(t)
				ctx := context.Background()
				id := createTestEnvironment(t, tx, newEnvironment(createTestCaller(t, tx, newCaller())))
				forceStatus(t, tx, id, from)

				err := mark[to](ctx, tx, id)
				got, getErr := GetEnvironment(ctx, tx, id)
				if getErr != nil {
					t.Fatalf("GetEnvironment() error = %v", getErr)
				}

				if !ok {
					if !errors.Is(err, ErrInvalidTransition) {
						t.Errorf("error = %v, want %v", err, ErrInvalidTransition)
					}
					if got.Status != from {
						t.Errorf("status = %s, want unchanged %s", got.Status, from)
					}
					return
				}
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				if got.Status != to {
					t.Errorf("status = %s, want %s", got.Status, to)
				}
				if (got.DeletedAt != nil) != (to == EnvDeleted) {
					t.Errorf("deleted_at = %v, want set only when %s", got.DeletedAt, EnvDeleted)
				}
				if to == EnvFailed && (got.FailureReason == nil || *got.FailureReason != "boom") {
					t.Errorf("failure_reason = %v, want boom", got.FailureReason)
				}
				if to != EnvFailed && got.FailureReason != nil {
					t.Errorf("failure_reason = %q, want nil", *got.FailureReason)
				}
			})
		}
	}
}

func TestEnvironmentTransitionNotFound(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	for name, fn := range map[string]func() error{
		"running":  func() error { return MarkEnvironmentRunning(ctx, tx, uuid.New()) },
		"failed":   func() error { return MarkEnvironmentFailed(ctx, tx, uuid.New(), "x") },
		"deleting": func() error { return MarkEnvironmentDeleting(ctx, tx, uuid.New()) },
		"deleted":  func() error { return MarkEnvironmentDeleted(ctx, tx, uuid.New()) },
	} {
		if err := fn(); !errors.Is(err, ErrEnvNotFound) {
			t.Errorf("%s: error = %v, want %v", name, err, ErrEnvNotFound)
		}
	}
}

func TestListExpiredEnvironments(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())

	expired := func(ttl time.Duration, status EnvStatus) uuid.UUID {
		e := newEnvironment(callerID)
		e.TTL = ttl
		id := createTestEnvironment(t, tx, e)
		forceStatus(t, tx, id, status)
		return id
	}
	older := expired(-2*time.Hour, EnvRunning)
	newer := expired(-time.Hour, EnvProvisioning)
	expired(-time.Hour, EnvDeleting)
	expired(-time.Hour, EnvDeleted)
	expired(-time.Hour, EnvFailed)
	expired(time.Hour, EnvRunning)
	exact := expired(0, EnvRunning)

	envs, err := ListExpiredEnvironments(ctx, tx)
	if err != nil {
		t.Fatalf("ListExpiredEnvironments() error = %v", err)
	}
	got := envIDs(envs)
	want := []uuid.UUID{older, newer, exact}
	if len(got) != len(want) {
		t.Fatalf("ListExpiredEnvironments() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListExpiredEnvironments()[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestListStuckProvisioning(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())

	create := func(age time.Duration, status EnvStatus) uuid.UUID {
		id := createTestEnvironment(t, tx, newEnvironment(callerID))
		forceStatus(t, tx, id, status)
		if _, err := tx.Exec(ctx, `UPDATE environments SET created_at = now() - make_interval(secs => $2) WHERE id = $1`, id, age.Seconds()); err != nil {
			t.Fatalf("set created_at: %v", err)
		}
		return id
	}
	oldest := create(10*time.Minute, EnvProvisioning)
	stuck := create(5*time.Minute, EnvProvisioning)
	create(4*time.Minute, EnvProvisioning)
	create(10*time.Minute, EnvRunning)
	create(10*time.Minute, EnvFailed)

	envs, err := ListStuckProvisioning(ctx, tx, 5*time.Minute)
	if err != nil {
		t.Fatalf("ListStuckProvisioning() error = %v", err)
	}
	got := envIDs(envs)
	if len(got) != 2 || got[0] != oldest || got[1] != stuck {
		t.Errorf("ListStuckProvisioning() = %v, want [%s %s]", got, oldest, stuck)
	}
}

func TestListDeletingEnvironments(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())

	var want uuid.UUID
	for _, s := range []EnvStatus{EnvProvisioning, EnvRunning, EnvDeleting, EnvDeleted, EnvFailed} {
		id := createTestEnvironment(t, tx, newEnvironment(callerID))
		forceStatus(t, tx, id, s)
		if s == EnvDeleting {
			want = id
		}
	}

	envs, err := ListDeletingEnvironments(ctx, tx)
	if err != nil {
		t.Fatalf("ListDeletingEnvironments() error = %v", err)
	}
	if got := envIDs(envs); len(got) != 1 || got[0] != want {
		t.Errorf("ListDeletingEnvironments() = %v, want [%s]", got, want)
	}
	if envs[0].Status != EnvDeleting {
		t.Errorf("status = %s, want %s", envs[0].Status, EnvDeleting)
	}
}

func TestListEnvironmentsEmpty(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	for name, fn := range map[string]func() ([]Environment, error){
		"expired":  func() ([]Environment, error) { return ListExpiredEnvironments(ctx, tx) },
		"stuck":    func() ([]Environment, error) { return ListStuckProvisioning(ctx, tx, 5*time.Minute) },
		"deleting": func() ([]Environment, error) { return ListDeletingEnvironments(ctx, tx) },
	} {
		envs, err := fn()
		if err != nil {
			t.Errorf("%s: error = %v", name, err)
		}
		if len(envs) != 0 {
			t.Errorf("%s: got %d environments, want 0", name, len(envs))
		}
	}
}
