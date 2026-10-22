-- Retention assistant reminder. Additive.
--
-- Administrators get a yearly 'privacy' notification when archived neighbors
-- have passed the retention period and could be anonymized. Nothing is ever
-- anonymized automatically.
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check
    CHECK (kind IN ('due', 'recurring', 'import', 'mail', 'backup', 'feedback', 'security', 'update', 'privacy'));
