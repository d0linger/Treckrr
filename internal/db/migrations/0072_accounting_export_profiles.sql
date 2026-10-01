-- Named mappings for the optional accounting CSV. They affect exports only.
CREATE TABLE accounting_export_profiles (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE,
    revenue_account    TEXT NOT NULL DEFAULT '',
    receivable_account TEXT NOT NULL DEFAULT '',
    tax_code           TEXT NOT NULL DEFAULT '',
    cost_center        TEXT NOT NULL DEFAULT '',
    columns            TEXT NOT NULL,
    delimiter          TEXT NOT NULL DEFAULT ';' CHECK (delimiter IN (';', ',')),
    decimal_comma      BOOLEAN NOT NULL DEFAULT true,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
