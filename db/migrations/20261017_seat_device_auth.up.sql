-- Seat-bound desktop sign-in.
--
-- A person holding a seat (or the licence owner, who holds seat #1)
-- signs a desktop app in by clicking an emailed link; the app never
-- sees the licence key. The resulting activation belongs to that seat,
-- is capped per seat, carries a rotating device secret, and must be
-- renewed by a fresh sign-in every reauth_days.

-- Plan policy. Existing plans get the defaults; the admin handler
-- applies the same defaults when a new plan omits them.
--   max_devices_per_seat      devices one seat may hold at once (<1 reads as 1)
--   max_device_moves_per_30d  sign-ins on a NEW device per seat in any rolling
--                             30 days, not counting the seat's first device
--   reauth_days               days a sign-in lasts before the device must sign
--                             in again (0 = never)
--   require_seat_signin       refuse licence-key activation for this plan
ALTER TABLE plans
    ADD COLUMN IF NOT EXISTS max_devices_per_seat INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS max_device_moves_per_30d INTEGER NOT NULL DEFAULT 2,
    ADD COLUMN IF NOT EXISTS reauth_days INTEGER NOT NULL DEFAULT 180,
    ADD COLUMN IF NOT EXISTS require_seat_signin BOOLEAN NOT NULL DEFAULT false;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'plans_seat_device_policy') THEN
        ALTER TABLE plans ADD CONSTRAINT plans_seat_device_policy
            CHECK (max_devices_per_seat >= 0 AND max_devices_per_seat <= 100
               AND max_device_moves_per_30d >= 0 AND max_device_moves_per_30d <= 1000
               AND reauth_days >= 0 AND reauth_days <= 3650);
    END IF;
END $$;

-- When true the owner only administers the licence: every seat goes to
-- an invitee and the owner cannot sign a desktop in.
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS owner_seat_released BOOLEAN NOT NULL DEFAULT false;

-- Seat binding on activations. seat_email is the holder's address,
-- lower-cased; NULL for a licence-key activation. The owner has no seat
-- row, so the address (not seats.id) is the binding.
ALTER TABLE activations
    ADD COLUMN IF NOT EXISTS seat_email TEXT,
    ADD COLUMN IF NOT EXISTS fingerprint TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS device_secret_hash TEXT,
    ADD COLUMN IF NOT EXISTS prev_device_secret_hash TEXT,
    ADD COLUMN IF NOT EXISTS secret_rotated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS signed_in_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_activations_seat
    ON activations (license_id, seat_email) WHERE seat_email IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_activations_device_secret
    ON activations (device_secret_hash) WHERE device_secret_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_activations_prev_device_secret
    ON activations (prev_device_secret_hash) WHERE prev_device_secret_hash IS NOT NULL;

-- One row per desktop sign-in attempt. The app holds request id + poll
-- secret; the email holds the link token. Only hashes are stored.
CREATE TABLE IF NOT EXISTS desktop_signin_requests (
    id               TEXT PRIMARY KEY,
    product_id       TEXT NOT NULL,
    email            TEXT NOT NULL,
    identifier       TEXT NOT NULL,
    label            TEXT NOT NULL DEFAULT '',
    fingerprint      TEXT NOT NULL DEFAULT '',
    link_token_hash  TEXT NOT NULL,
    poll_secret_hash TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'approved', 'refused', 'consumed')),
    refuse_code      TEXT NOT NULL DEFAULT '',
    next_move_at     TIMESTAMPTZ,
    license_id       TEXT,
    activation_id    TEXT,
    created_ip       TEXT NOT NULL DEFAULT '',
    expires_at       TIMESTAMPTZ NOT NULL,
    approved_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_desktop_signin_link_token
    ON desktop_signin_requests (link_token_hash);
CREATE INDEX IF NOT EXISTS idx_desktop_signin_email_created
    ON desktop_signin_requests (email, created_at);

-- Every sign-in on a device new to the seat. is_move marks the ones the
-- move limit counts: a device different from the seat's previous one.
CREATE TABLE IF NOT EXISTS seat_device_events (
    id         TEXT PRIMARY KEY,
    license_id TEXT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    email      TEXT NOT NULL,
    identifier TEXT NOT NULL,
    is_move    BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_seat_device_events_seat
    ON seat_device_events (license_id, email, created_at);
