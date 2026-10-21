// Package store contains all PostgreSQL queries.
package store

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the pgx pool with query methods.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a Store.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool (used by migrations/health checks).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
