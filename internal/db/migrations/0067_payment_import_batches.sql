-- Durable provenance for bank imports. Existing deduplication hashes and
-- payments stay untouched; historical imports cannot be linked reliably and
-- therefore remain without a batch.
CREATE TABLE payment_import_batches (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_sha256   TEXT NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    uploaded_by     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    status          TEXT NOT NULL DEFAULT 'processing'
                    CHECK (status IN ('processing', 'completed', 'failed')),
    total_rows      INTEGER NOT NULL DEFAULT 0 CHECK (total_rows >= 0),
    booked_rows     INTEGER NOT NULL DEFAULT 0 CHECK (booked_rows >= 0),
    skipped_rows    INTEGER NOT NULL DEFAULT 0 CHECK (skipped_rows >= 0),
    duplicate_rows  INTEGER NOT NULL DEFAULT 0 CHECK (duplicate_rows >= 0),
    unmatched_rows  INTEGER NOT NULL DEFAULT 0 CHECK (unmatched_rows >= 0),
    reversed_rows   INTEGER NOT NULL DEFAULT 0 CHECK (reversed_rows >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ
);
CREATE INDEX idx_payment_import_batches_created
    ON payment_import_batches (created_at DESC);

CREATE TABLE payment_import_rows (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    batch_id          BIGINT NOT NULL REFERENCES payment_import_batches(id) ON DELETE RESTRICT,
    row_no            INTEGER NOT NULL CHECK (row_no > 0),
    transaction_hash  TEXT NOT NULL,
    transaction_date  DATE,
    amount             NUMERIC(14,4) NOT NULL CHECK (amount > 0),
    reference          TEXT NOT NULL DEFAULT '',
    payer_name         TEXT NOT NULL DEFAULT '',
    payer_iban         TEXT NOT NULL DEFAULT '',
    match_method       TEXT NOT NULL DEFAULT '',
    billing_year_id    BIGINT REFERENCES billing_years(id) ON DELETE SET NULL,
    neighbor_id        BIGINT REFERENCES neighbors(id) ON DELETE SET NULL,
    invoice_id         BIGINT REFERENCES invoices(id) ON DELETE SET NULL,
    status             TEXT NOT NULL
                       CHECK (status IN ('pending', 'booked', 'duplicate', 'unmatched', 'skipped', 'reversed')),
    reason             TEXT NOT NULL DEFAULT '',
    payment_id         BIGINT REFERENCES payments(id) ON DELETE SET NULL,
    reversal_payment_id BIGINT REFERENCES payments(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (batch_id, row_no)
);
CREATE INDEX idx_payment_import_rows_batch ON payment_import_rows (batch_id, row_no);
CREATE UNIQUE INDEX idx_payment_import_rows_payment ON payment_import_rows (payment_id)
    WHERE payment_id IS NOT NULL;
CREATE UNIQUE INDEX idx_payment_import_rows_reversal ON payment_import_rows (reversal_payment_id)
    WHERE reversal_payment_id IS NOT NULL;

ALTER TABLE payment_imports
    ADD COLUMN batch_id   BIGINT REFERENCES payment_import_batches(id) ON DELETE SET NULL,
    ADD COLUMN row_id     BIGINT REFERENCES payment_import_rows(id) ON DELETE SET NULL,
    ADD COLUMN payment_id BIGINT REFERENCES payments(id) ON DELETE SET NULL;

ALTER TABLE payments
    ADD COLUMN reversal_of_payment_id BIGINT REFERENCES payments(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX idx_payments_one_reversal
    ON payments (reversal_of_payment_id)
    WHERE reversal_of_payment_id IS NOT NULL;
