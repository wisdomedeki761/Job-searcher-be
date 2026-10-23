// Package seeder seeds the initial super admin from environment variables.
package seeder

import (
	"context"
	"fmt"
	"log/slog"

	"qx-scraper/internal/auth"
	"qx-scraper/internal/config"
	"qx-scraper/internal/store"
)

// SeedSuperAdmin creates the super admin from SUPER_ADMIN_* env vars on first
// start. If a super admin already exists in the database it does nothing —
// credentials are never overwritten or re-seeded.
//
// It fails (returns error) when no super admin exists AND the env credentials
// are missing, because starting without a way to log in would leave the
// system unmanageable.
func SeedSuperAdmin(ctx context.Context, st *store.Store, cfg *config.Config, log *slog.Logger) error {
	n, err := st.CountSuperAdmins(ctx)
	if err != nil {
		return fmt.Errorf("count super admins: %w", err)
	}
	if n > 0 {
		log.Info("super admin already present — seed skipped", "count", n)
		return nil
	}

	if cfg.SuperAdminEmail == "" || cfg.SuperAdminPassword == "" {
		return fmt.Errorf(
			"no super admin in database and SUPER_ADMIN_EMAIL / SUPER_ADMIN_PASSWORD are not set; " +
				"set them in .env so the first super admin can be seeded")
	}

	hash, err := auth.HashPassword(cfg.SuperAdminPassword)
	if err != nil {
		return fmt.Errorf("hash super admin password: %w", err)
	}

	admin, err := st.CreateAdmin(ctx, cfg.SuperAdminEmail, hash, cfg.SuperAdminName, "super_admin", true)
	if err != nil {
		return fmt.Errorf("create super admin: %w", err)
	}

	log.Info("super admin seeded", "email", admin.Email, "id", admin.ID)
	return nil
}
