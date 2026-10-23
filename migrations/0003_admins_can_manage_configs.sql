-- Per-user permission: may this account create/edit search configs?
-- FALSE = can only view the job feed. Existing super admins (the only
-- accounts in v1) automatically get full access.
ALTER TABLE admins ADD COLUMN IF NOT EXISTS can_manage_configs BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE admins SET can_manage_configs = TRUE WHERE role = 'super_admin';
