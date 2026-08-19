-- 0036: make the audit log append-only for tamper-evidence.
--
-- A BEFORE UPDATE OR DELETE trigger blocks every mutation of audit_log: UPDATE is
-- always forbidden, and DELETE is forbidden UNLESS the current transaction has
-- explicitly opted in via the treckrr.allow_audit_prune session flag. The flag is
-- set with SET LOCAL by the staggered-retention purge (PurgeAuditLog), so the ONLY
-- code path that can delete an audit row is that one deliberate maintenance job —
-- everything else (a stray query, a compromised handler, even a manual UPDATE by
-- the app's own DB user) is rejected at the database level.
--
-- This holds regardless of the connecting role: the guard runs inside the table's
-- own trigger, so it cannot be bypassed by the app user even though that user owns
-- the table. It makes the audit trail trustworthy as evidence: an entry, once
-- written, cannot be silently altered or removed outside the retention policy.
CREATE OR REPLACE FUNCTION audit_log_guard() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('treckrr.allow_audit_prune', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only (% not permitted)', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_log_guard_trg ON audit_log;
CREATE TRIGGER audit_log_guard_trg
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_guard();
