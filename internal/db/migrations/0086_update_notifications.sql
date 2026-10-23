-- Optional release notice (UPDATE_CHECK=true): administrators get one
-- notification per newer release. Additive; with the check off nothing changes.
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check
    CHECK (kind IN ('due', 'recurring', 'import', 'mail', 'backup', 'feedback', 'security', 'update'));
