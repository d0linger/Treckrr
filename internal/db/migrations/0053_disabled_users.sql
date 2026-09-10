-- Preserve immutable audit attribution while retiring an account. Existing
-- accounts stay active; no history or credentials are rewritten by migration.
ALTER TABLE users ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT false;
