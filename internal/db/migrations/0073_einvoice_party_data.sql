-- Optional structured party data for ebInterface 6.1. Existing free-form
-- invoice addresses remain untouched and all current workflows stay valid.
ALTER TABLE company
    ADD COLUMN einvoice_street TEXT NOT NULL DEFAULT '',
    ADD COLUMN einvoice_zip TEXT NOT NULL DEFAULT '',
    ADD COLUMN einvoice_town TEXT NOT NULL DEFAULT '',
    ADD COLUMN einvoice_country_code TEXT NOT NULL DEFAULT 'AT';

ALTER TABLE neighbors
    ADD COLUMN einvoice_street TEXT NOT NULL DEFAULT '',
    ADD COLUMN einvoice_zip TEXT NOT NULL DEFAULT '',
    ADD COLUMN einvoice_town TEXT NOT NULL DEFAULT '',
    ADD COLUMN einvoice_country_code TEXT NOT NULL DEFAULT 'AT',
    ADD COLUMN einvoice_order_id TEXT NOT NULL DEFAULT '';

ALTER TABLE company ADD CONSTRAINT company_einvoice_country_code_shape
    CHECK (einvoice_country_code ~ '^[A-Z]{2}$');
ALTER TABLE neighbors ADD CONSTRAINT neighbors_einvoice_country_code_shape
    CHECK (einvoice_country_code ~ '^[A-Z]{2}$');
