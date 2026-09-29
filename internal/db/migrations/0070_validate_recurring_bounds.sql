-- Validate after 0069 committed so the add-constraint migration releases its
-- catalog lock before PostgreSQL scans historical rows.
ALTER TABLE recurring_entries
    VALIDATE CONSTRAINT recurring_entries_schedule_bounds_chk;
