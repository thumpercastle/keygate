-- The seat count a licence had before an upgrade, restored if it reverts.
ALTER TABLE license_upgrades ADD COLUMN IF NOT EXISTS from_quantity INTEGER NOT NULL DEFAULT 1;
