-- Remove quantity bounds and stored purchase quantity when reverting
-- per-seat subscription support.
ALTER TABLE plans DROP CONSTRAINT IF EXISTS plans_quantity_bounds;
ALTER TABLE plans DROP CONSTRAINT IF EXISTS plans_per_seat_subscription;
ALTER TABLE plans DROP COLUMN IF EXISTS per_seat;
ALTER TABLE plans DROP COLUMN IF EXISTS min_quantity;
ALTER TABLE plans DROP COLUMN IF EXISTS max_quantity;

ALTER TABLE licenses DROP CONSTRAINT IF EXISTS licenses_quantity_positive;
ALTER TABLE licenses DROP COLUMN IF EXISTS quantity;
