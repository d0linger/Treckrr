-- Tamper-evident audit chain. Additive.
--
-- 0036 makes audit_log append-only for the application, but a trigger stops
-- the app, not a database owner or superuser. A hash chain makes a later edit
-- or deletion detectable: each sealed row stores the hash of its predecessor
-- and a SHA-256 over its own canonical content.
--
-- Rows are inserted unsealed, exactly as before, and the maintenance tick seals
-- them in commit-visible order (chain_seq), so audit inserts never wait on a
-- shared chain lock. There is one chain per retention segment ('short' auth
-- and ops noise, 'long' business records), so the staggered purge removes a
-- prefix of each chain and records the hash it ends on as the new anchor. The
-- heads travel with every pg_dump backup; the restore rehearsal copies the
-- heads of the restored backup into audit_chain_backup_heads, and the nightly
-- verification checks that the live chain still contains them.
ALTER TABLE audit_log
    ADD COLUMN chain TEXT CHECK (chain IN ('short', 'long')),
    ADD COLUMN chain_seq BIGINT,
    ADD COLUMN prev_hash BYTEA,
    ADD COLUMN row_hash BYTEA,
    ADD CONSTRAINT audit_log_sealed_complete CHECK (
        (row_hash IS NULL AND chain IS NULL AND chain_seq IS NULL AND prev_hash IS NULL)
        OR (row_hash IS NOT NULL AND chain IS NOT NULL AND chain_seq IS NOT NULL AND prev_hash IS NOT NULL));

CREATE UNIQUE INDEX idx_audit_log_chain_seq ON audit_log(chain, chain_seq) WHERE chain_seq IS NOT NULL;
CREATE INDEX idx_audit_log_unsealed ON audit_log(id) WHERE row_hash IS NULL;

CREATE TABLE audit_chain_heads (
    chain TEXT PRIMARY KEY CHECK (chain IN ('short', 'long')),
    last_seq BIGINT NOT NULL DEFAULT 0,
    last_hash BYTEA NOT NULL,
    -- Rows up to anchor_seq were purged; anchor_hash is the row_hash of the
    -- last purged row, i.e. the prev_hash the first remaining row must carry.
    anchor_seq BIGINT NOT NULL DEFAULT 0,
    anchor_hash BYTEA NOT NULL,
    verified_at TIMESTAMPTZ,
    verified_ok BOOLEAN,
    verify_note TEXT NOT NULL DEFAULT ''
);
INSERT INTO audit_chain_heads (chain, last_hash, anchor_hash)
VALUES ('short', decode(repeat('00', 32), 'hex'), decode(repeat('00', 32), 'hex')),
       ('long',  decode(repeat('00', 32), 'hex'), decode(repeat('00', 32), 'hex'));

CREATE TABLE audit_chain_backup_heads (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chain TEXT NOT NULL CHECK (chain IN ('short', 'long')),
    seq BIGINT NOT NULL,
    hash BYTEA NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (chain, seq, hash)
);

-- The guard now also admits the sealing UPDATE: only when the transaction opts
-- in, only on an unsealed row, and only if every recorded column stays as it
-- was. Content edits and re-sealing remain impossible for the application.
CREATE OR REPLACE FUNCTION audit_log_guard() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('treckrr.allow_audit_prune', true) = 'on' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE'
       AND current_setting('treckrr.allow_audit_seal', true) = 'on'
       AND OLD.row_hash IS NULL AND NEW.row_hash IS NOT NULL
       AND (NEW.id, NEW.user_id, NEW.username, NEW.action, NEW.entity, NEW.entity_id, NEW.detail, NEW.ip, NEW.created_at)
           IS NOT DISTINCT FROM
           (OLD.id, OLD.user_id, OLD.username, OLD.action, OLD.entity, OLD.entity_id, OLD.detail, OLD.ip, OLD.created_at) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only (% not permitted)', TG_OP;
END;
$$ LANGUAGE plpgsql;
