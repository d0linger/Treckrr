-- Run ONCE on the old schema with psql -XqAt -f <this file> > fingerprint.sql.
-- Run the generated fingerprint.sql before AND after the upgrade with -XqAt.
-- It preserves the old column list, so additive columns do not mask changes to
-- existing values. Stop all writers first; see docs/upgrade-validation.md.
\set ON_ERROR_STOP on

-- Refuse an unrelated schema. Completeness requires comparison with a known
-- live baseline; an existing installation may legitimately have empty tables.
SELECT 1 / CASE WHEN to_regclass('public.entries') IS NOT NULL
                     AND to_regclass('public.invoices') IS NOT NULL
                     AND to_regclass('public.payments') IS NOT NULL
                THEN 1 ELSE 0 END AS core_schema_present
\gset

SELECT $$\set ON_ERROR_STOP on$$;
SELECT 'BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;';

SELECT format($query$
SELECT %L AS table_name, count(*) AS row_count,
       encode(sha256(convert_to(coalesce(string_agg(row_hash, '' ORDER BY row_hash), ''), 'UTF8')), 'hex') AS sha256
FROM (
    SELECT encode(sha256(convert_to(to_jsonb(v)::text, 'UTF8')), 'hex') AS row_hash
    FROM (SELECT %s FROM public.%I) AS v
) AS hashes;
$query$, c.relname, string_agg(format('%I', a.attname), ', ' ORDER BY a.attnum), c.relname)
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
  AND c.relname IN (
    'company', 'price_bases', 'load_levels', 'tractors', 'machines', 'gespanne', 'gespann_machines',
    'neighbors', 'billing_years', 'billing_year_neighbors', 'entries', 'entry_machines',
    'entry_machine_snapshots', 'neighbor_ledger', 'invoices', 'invoice_entries', 'payments',
    'payment_plans', 'dunning_notices', 'persons', 'neighbor_equipment', 'fuel_adjustments',
    'neighbor_price_terms', 'neighbor_fields', 'entry_signatures', 'entry_photos', 'ledger_photos',
    'accounting_export_profiles', 'recurring_entries', 'recurring_exceptions', 'payment_imports',
    'payment_import_batches', 'payment_import_rows'
  )
GROUP BY c.oid, c.relname
ORDER BY c.relname;

SELECT 'COMMIT;';
