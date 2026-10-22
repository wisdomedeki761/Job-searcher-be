package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"qx-scraper/internal/model"
)

// CountSuperAdmins returns how many super admin accounts exist.
// Seeding only happens when this is zero.
func (s *Store) CountSuperAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM admins WHERE role = 'super_admin'`).Scan(&n)
	return n, err
}

// CreateAdmin inserts a new admin account.
func (s *Store) CreateAdmin(ctx context.Context, email, passwordHash, name, role string) (*model.Admin, error) {
	a := &model.Admin{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO admins (id, email, password_hash, name, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, email, password_hash, name, role, last_login_at, created_at, updated_at`,
		uuid.New(), email, passwordHash, name, role,
	).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Name, &a.Role, &a.LastLoginAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create admin: %w", err)
	}
	return a, nil
}

// GetAdminByEmail fetches an admin by email (including password hash for login).
func (s *Store) GetAdminByEmail(ctx context.Context, email string) (*model.Admin, error) {
	a := &model.Admin{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, name, role, last_login_at, created_at, updated_at
		FROM admins WHERE email = $1`, email,
	).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Name, &a.Role, &a.LastLoginAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return a, nil
}

// GetAdminByID fetches an admin by ID.
func (s *Store) GetAdminByID(ctx context.Context, id uuid.UUID) (*model.Admin, error) {
	a := &model.Admin{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, name, role, last_login_at, created_at, updated_at
		FROM admins WHERE id = $1`, id,
	).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Name, &a.Role, &a.LastLoginAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return a, nil
}

// TouchLogin updates last_login_at.
func (s *Store) TouchLogin(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE admins SET last_login_at = now(), updated_at = now() WHERE id = $1`, id)
	return err
}
