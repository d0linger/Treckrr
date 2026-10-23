-- Billing increment and minimum charge per rate basis. Additive.
--
-- Hours were billed exactly as typed. A group that bills machine hours in
-- steps (e.g. quarter hours) or with a minimum per job had to round by hand.
-- Both default to 0, which keeps every price exactly as before. They apply
-- when catalog machine hours are booked or edited; existing open bookings
-- change only through the recalc preview, after review.
ALTER TABLE price_bases
    ADD COLUMN billing_increment_h NUMERIC(6,3) NOT NULL DEFAULT 0
        CHECK (billing_increment_h >= 0 AND billing_increment_h <= 24),
    ADD COLUMN minimum_h NUMERIC(6,3) NOT NULL DEFAULT 0
        CHECK (minimum_h >= 0 AND minimum_h <= 24);

-- A changed rule can reprice open bookings, so it moves the basis revision the
-- recalc gate (0040) compares priced_at against, like an edited price item.
CREATE OR REPLACE FUNCTION treckrr_touch_billing_rule() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.billing_increment_h IS DISTINCT FROM OLD.billing_increment_h
       OR NEW.minimum_h IS DISTINCT FROM OLD.minimum_h THEN
        NEW.items_updated_at := now();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_price_bases_billing_rule
    BEFORE UPDATE OF billing_increment_h, minimum_h ON price_bases
    FOR EACH ROW EXECUTE FUNCTION treckrr_touch_billing_rule();
