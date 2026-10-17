-- Plans may sell subscription seats in bounded quantities; store the
-- purchased quantity on each license so limits can be enforced locally.
ALTER TABLE plans
    ADD COLUMN IF NOT EXISTS per_seat BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS min_quantity INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS max_quantity INTEGER NOT NULL DEFAULT 1;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'plans_quantity_bounds') THEN
        ALTER TABLE plans ADD CONSTRAINT plans_quantity_bounds
            CHECK (min_quantity >= 1 AND max_quantity >= min_quantity AND max_quantity <= 10000);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'plans_per_seat_subscription') THEN
        ALTER TABLE plans ADD CONSTRAINT plans_per_seat_subscription
            CHECK (NOT per_seat OR license_type = 'subscription');
    END IF;
END $$;

ALTER TABLE licenses ADD COLUMN IF NOT EXISTS quantity INTEGER NOT NULL DEFAULT 1;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'licenses_quantity_positive') THEN
        ALTER TABLE licenses ADD CONSTRAINT licenses_quantity_positive CHECK (quantity >= 1);
    END IF;
END $$;
