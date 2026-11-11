package db

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func pageIDs(t *testing.T, q DBTX, callerID uuid.UUID, f EnvironmentFilter) [][]uuid.UUID {
	t.Helper()
	var pages [][]uuid.UUID
	for {
		page, err := ListCallerEnvironments(context.Background(), q, callerID, f)
		if err != nil {
			t.Fatalf("ListCallerEnvironments() error = %v", err)
		}
		if len(page) == 0 {
			return pages
		}
		pages = append(pages, envIDs(page))
		f.Offset += f.Limit
	}
}

func TestListCallerEnvironmentsPaged(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	callerID := createTestCaller(t, tx, newCaller())
	other := newCaller()
	other.GithubID = new("other")
	createTestEnvironment(t, tx, newEnvironment(createTestCaller(t, tx, other)))

	var newestFirst []uuid.UUID
	statuses := []EnvStatus{EnvRunning, EnvFailed, EnvRunning, EnvDeleted, EnvRunning}
	for i, s := range statuses {
		id := createTestEnvironment(t, tx, newEnvironment(callerID))
		forceStatus(t, tx, id, s)
		if _, err := tx.Exec(ctx, `UPDATE environments SET created_at = created_at - make_interval(mins => $2) WHERE id = $1`, id, i); err != nil {
			t.Fatalf("age env: %v", err)
		}
		newestFirst = append(newestFirst, id)
	}

	pages := pageIDs(t, tx, callerID, EnvironmentFilter{Limit: 2})
	if want := [][]uuid.UUID{newestFirst[0:2], newestFirst[2:4], newestFirst[4:5]}; !slices.EqualFunc(pages, want, slices.Equal) {
		t.Errorf("pages = %v, want %v", pages, want)
	}

	running := pageIDs(t, tx, callerID, EnvironmentFilter{Status: EnvRunning, Limit: 2})
	if want := [][]uuid.UUID{{newestFirst[0], newestFirst[2]}, {newestFirst[4]}}; !slices.EqualFunc(running, want, slices.Equal) {
		t.Errorf("running pages = %v, want %v", running, want)
	}
}

func TestListCallerEnvironmentsSameCreatedAt(t *testing.T) {
	tx := testTx(t)
	callerID := createTestCaller(t, tx, newCaller())
	var all []uuid.UUID
	for range 5 {
		all = append(all, createTestEnvironment(t, tx, newEnvironment(callerID)))
	}

	var seen []uuid.UUID
	for _, page := range pageIDs(t, tx, callerID, EnvironmentFilter{Limit: 2}) {
		seen = append(seen, page...)
	}
	slices.SortFunc(all, func(a, b uuid.UUID) int { return compareUUID(a, b) })
	slices.SortFunc(seen, func(a, b uuid.UUID) int { return compareUUID(a, b) })
	if !slices.Equal(seen, all) {
		t.Errorf("paged ids = %v, want each of %v exactly once", seen, all)
	}
}

func compareUUID(a, b uuid.UUID) int {
	for i := range a {
		if a[i] != b[i] {
			return int(a[i]) - int(b[i])
		}
	}
	return 0
}

func TestCountCallerEnvironments(t *testing.T) {
	tx := testTx(t)
	callerID := createTestCaller(t, tx, newCaller())
	for _, s := range []EnvStatus{EnvRunning, EnvRunning, EnvFailed} {
		forceStatus(t, tx, createTestEnvironment(t, tx, newEnvironment(callerID)), s)
	}
	createTestEnvironment(t, tx, newEnvironment(callerID))

	counts, err := CountCallerEnvironments(context.Background(), tx, callerID, nil)
	if err != nil {
		t.Fatalf("CountCallerEnvironments() error = %v", err)
	}
	want := map[EnvStatus]int{EnvRunning: 2, EnvFailed: 1, EnvProvisioning: 1}
	if len(counts) != len(want) {
		t.Errorf("counts = %v, want %v", counts, want)
	}
	for s, n := range want {
		if counts[s] != n {
			t.Errorf("counts[%s] = %d, want %d", s, counts[s], n)
		}
	}
}
