package db

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestCreateAdmin(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	id, err := CreateAdmin(ctx, tx, "root", []byte("hash"))
	if err != nil {
		t.Fatalf("CreateAdmin() error = %v", err)
	}

	got, err := AdminByUsername(ctx, tx, "root")
	if err != nil {
		t.Fatalf("AdminByUsername() error = %v", err)
	}
	if got.ID != id || got.Username != "root" || !bytes.Equal(got.PasswordHash, []byte("hash")) {
		t.Errorf("AdminByUsername() = %+v, want id %s, root, hash", got, id)
	}
}

func TestCreateAdminDuplicateUsername(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	if _, err := CreateAdmin(ctx, tx, "root", []byte("first")); err != nil {
		t.Fatalf("CreateAdmin() error = %v", err)
	}
	if _, err := CreateAdmin(ctx, tx, "root", []byte("second")); !errors.Is(err, ErrAdminExists) {
		t.Fatalf("CreateAdmin() duplicate error = %v, want %v", err, ErrAdminExists)
	}

	got, err := AdminByUsername(ctx, tx, "root")
	if err != nil {
		t.Fatalf("AdminByUsername() error = %v", err)
	}
	if !bytes.Equal(got.PasswordHash, []byte("first")) {
		t.Errorf("password hash = %q, want unchanged first", got.PasswordHash)
	}
}

func TestAdminByUsernameNotFound(t *testing.T) {
	tx := testTx(t)
	if _, err := AdminByUsername(context.Background(), tx, "nobody"); !errors.Is(err, ErrAdminNotFound) {
		t.Errorf("AdminByUsername() error = %v, want %v", err, ErrAdminNotFound)
	}
}
