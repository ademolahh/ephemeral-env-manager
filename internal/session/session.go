package session

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/ademolahh/ephemeral-env-manager/internal/auth"
)

var ErrNotFound = errors.New("session not found")

type Session struct {
	CallerID  *uuid.UUID `json:"caller_id,omitempty"`
	AdminID   *uuid.UUID `json:"admin_id,omitempty"`
	ExpiresAt time.Time  `json:"expires_at"`
}

type Store struct {
	rdb    *redis.Client
	prefix string
}

func New(rdb *redis.Client, prefix string) *Store {
	return &Store{rdb: rdb, prefix: prefix}
}

func Connect(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	rdb := redis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return rdb, nil
}

func (s *Store) key(token string) string {
	return s.prefix + "session:" + hex.EncodeToString(auth.HashToken(token))
}

func (s *Store) CreateCaller(ctx context.Context, callerID uuid.UUID, ttl time.Duration) (string, time.Time, error) {
	return s.create(ctx, Session{CallerID: &callerID}, ttl)
}

func (s *Store) CreateAdmin(ctx context.Context, adminID uuid.UUID, ttl time.Duration) (string, time.Time, error) {
	return s.create(ctx, Session{AdminID: &adminID}, ttl)
}

func (s *Store) create(ctx context.Context, sess Session, ttl time.Duration) (string, time.Time, error) {
	sess.ExpiresAt = time.Now().Add(ttl).UTC().Truncate(time.Second)
	body, err := json.Marshal(sess)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}

	token := auth.RandomString()
	if err := s.rdb.Set(ctx, s.key(token), body, ttl).Err(); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return token, sess.ExpiresAt, nil
}

func (s *Store) Get(ctx context.Context, token string) (Session, error) {
	body, err := s.rdb.Get(ctx, s.key(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}

	var sess Session
	if err := json.Unmarshal(body, &sess); err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	return sess, nil
}

func (s *Store) Delete(ctx context.Context, token string) error {
	n, err := s.rdb.Del(ctx, s.key(token)).Result()
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
