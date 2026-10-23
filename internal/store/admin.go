package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"qx-scraper/internal/model"
)

// ErrEmailTaken reports an email that already belongs to another account.
var ErrEmailTaken = errors.New("email already in use")

const adminCols = `id, email, password_hash, name, role, can_manage_configs, last_login_at, created_at, updated_at`

func scanAdmin(row pgx.Row) (*model.Admin, error) {
	a := &model.Admin{}
	err := row.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Name, &a.Role,
		&a.CanManageConfigs, &a.LastLoginAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return a, nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (used to map duplicate emails to ErrEmailTaken).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CountSuperAdmins returns how many super admin accounts exist.
// Seeding only happens when this is zero; deletion/demotion is blocked at 1.
func (s *Store) CountSuperAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM admins WHERE role = 'super_admin'`).Scan(&n)
	return n, err
}

// CreateAdmin inserts a new admin account.
func (s *Store) CreateAdmin(ctx context.Context, email, passwordHash, name, role string, canManageConfigs bool) (*model.Admin, error) {
	a := &model.Admin{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO admins (id, email, password_hash, name, role, can_manage_configs)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+adminCols,
		uuid.New(), email, passwordHash, name, role, canManageConfigs,
	).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Name, &a.Role,
		&a.CanManageConfigs, &a.LastLoginAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("create admin: %w", err)
	}
	return a, nil
}

// GetAdminByEmail fetches an admin by email (including password hash for login).
func (s *Store) GetAdminByEmail(ctx context.Context, email string) (*model.Admin, error) {
	return scanAdmin(s.pool.QueryRow(ctx,
		`SELECT `+adminCols+` FROM admins WHERE email = $1`, email))
}

// GetAdminByID fetches an admin by ID.
func (s *Store) GetAdminByID(ctx context.Context, id uuid.UUID) (*model.Admin, error) {
	return scanAdmin(s.pool.QueryRow(ctx,
		`SELECT `+adminCols+` FROM admins WHERE id = $1`, id))
}

// ListAdmins returns every account, newest first.
func (s *Store) ListAdmins(ctx context.Context) ([]*model.Admin, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+adminCols+` FROM admins ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list admins: %w", err)
	}
	defer rows.Close()

	var out []*model.Admin
	for rows.Next() {
		a := &model.Admin{}
		if err := rows.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Name, &a.Role,
			&a.CanManageConfigs, &a.LastLoginAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan admin: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAdminProfile changes an account's email and/or display name.
// Empty values are left untouched. Returns ErrEmailTaken on a duplicate email.
func (s *Store) UpdateAdminProfile(ctx context.Context, id uuid.UUID, email, name string) (*model.Admin, error) {
	a := &model.Admin{}
	err := s.pool.QueryRow(ctx, `
		UPDATE admins SET
			email  = COALESCE(NULLIF($2, ''), email),
			name   = COALESCE(NULLIF($3, ''), name),
			updated_at = now()
		WHERE id = $1
		RETURNING `+adminCols, id, email, name,
	).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.Name, &a.Role,
		&a.CanManageConfigs, &a.LastLoginAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, mapNotFound(err)
	}
	return a, nil
}

// UpdateAdminPermissions toggles search-config access for an account.
func (s *Store) UpdateAdminPermissions(ctx context.Context, id uuid.UUID, canManageConfigs bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE admins SET can_manage_configs = $2, updated_at = now() WHERE id = $1`,
		id, canManageConfigs)
	return mapNotFound(err)
}

// UpdateAdminPassword replaces the bcrypt hash of an account.
func (s *Store) UpdateAdminPassword(ctx context.Context, id uuid.UUID, passwordHash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE admins SET password_hash = $2, updated_at = now() WHERE id = $1`,
		id, passwordHash)
	return mapNotFound(err)
}

// DeleteAdmin removes an account.
func (s *Store) DeleteAdmin(ctx context.Context, id uuid.UUID) error {
	return mapNotFound(func() error {
		_, err := s.pool.Exec(ctx, `DELETE FROM admins WHERE id = $1`, id)
		return err
	}())
}

// TouchLogin updates last_login_at.
func (s *Store) TouchLogin(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE admins SET last_login_at = now(), updated_at = now() WHERE id = $1`, id)
	return err
}
