-- 0036: make the audit log append-only for tamper-evidence.
--
-- A BEFORE UPDATE OR DELETE trigger blocks every mutation of audit_log: UPDATE is
-- always forbidden, and DELETE is forbidden UNLESS the current transaction has
-- explicitly opted in via the treckrr.allow_audit_prune session flag, set with
-- SET LOCAL by the staggered-retention purge (PurgeAuditLog). So no ordinary code
-- path can delete or alter an audit row: a stray query, a buggy handler, or a
-- fat-fingered manual UPDATE is rejected at the database level, and the one
-- deliberate maintenance job is the sole exception.
--
-- Threat model — read this before relying on it. This is accident-prevention and
-- tamper-EVIDENCE, not a hard authorization boundary. Treckrr connects with a
-- single role that owns this table (it also runs the migrations), and a table
-- owner can always ALTER TABLE ... DISABLE TRIGGER or DROP the trigger and then
-- mutate freely. The opt-in GUC is therefore NOT an authorization check against a
-- hostile owner role — an attacker who already has the app's DB credentials can
-- bypass it, and in a single-role deployment no trigger-based scheme (GUC or a
-- SECURITY DEFINER routine owned by that same role) changes that. What the guard
-- buys is that the trail cannot be silently rewritten by normal application code
-- or an operator's slip: any real removal must go through the one auditable path.
-- A genuine privilege boundary would require a second maintenance role that owns
-- the table while the app connects as a non-owner with no DELETE/DDL rights; that
-- is a deployment/credential change, out of scope for this schema migration.
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
