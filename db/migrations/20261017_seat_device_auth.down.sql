-- Revert seat-bound desktop sign-in. Seat-bound activations become
-- ordinary activations (their secrets are dropped, so those devices must
-- activate again with a licence key).
DROP TABLE IF EXISTS seat_device_events;
DROP TABLE IF EXISTS desktop_signin_requests;

DROP INDEX IF EXISTS idx_activations_prev_device_secret;
DROP INDEX IF EXISTS idx_activations_device_secret;
DROP INDEX IF EXISTS idx_activations_seat;
ALTER TABLE activations
    DROP COLUMN IF EXISTS signed_in_at,
    DROP COLUMN IF EXISTS secret_rotated_at,
    DROP COLUMN IF EXISTS prev_device_secret_hash,
    DROP COLUMN IF EXISTS device_secret_hash,
    DROP COLUMN IF EXISTS fingerprint,
    DROP COLUMN IF EXISTS seat_email;

ALTER TABLE licenses DROP COLUMN IF EXISTS owner_seat_released;

ALTER TABLE plans DROP CONSTRAINT IF EXISTS plans_seat_device_policy;
ALTER TABLE plans
    DROP COLUMN IF EXISTS require_seat_signin,
    DROP COLUMN IF EXISTS reauth_days,
    DROP COLUMN IF EXISTS max_device_moves_per_30d,
    DROP COLUMN IF EXISTS max_devices_per_seat;
