-- Synthetic data only. Run on empty disposable comparison databases after migrations.
DO $$ BEGIN
  IF current_database() NOT IN ('comparison_before', 'comparison_after') THEN
    RAISE EXCEPTION 'Refusing to seed a non-comparison database';
  END IF;
  IF EXISTS (SELECT 1 FROM billing_years) THEN
    RAISE EXCEPTION 'Comparison fixture requires an empty database';
  END IF;
END $$;
UPDATE users SET must_change_password = false;
UPDATE company SET name = 'Demo-Hof Bergmann', address = E'Feldweg 3\n4780 Musterdorf', tax_mode = 'kleinunternehmer';
INSERT INTO price_bases (year, name) VALUES (2025, 'Demo-Basis 2025');
INSERT INTO billing_years (year, base_id, label, status) VALUES (2025, 1, '2025', 'open');
INSERT INTO neighbors (name, address) VALUES
  ('Demo-Nachbar Steiner', ''),
  ('Demo-Nachbar Huber', E'Wiesenweg 4\n4780 Musterdorf'),
  ('Demo-Nachbar Berger', E'Dorfweg 5\n4780 Musterdorf');
INSERT INTO billing_year_neighbors (billing_year_id, neighbor_id) VALUES (1, 1), (1, 2), (1, 3);
INSERT INTO load_levels (base_id, name, cost_per_ps) VALUES (1, 'mittel', 0.36);
INSERT INTO tractors (base_id, ident, name, ps) VALUES (1, 'T1', 'Demo-Traktor', 100);
INSERT INTO machines (base_id, name, working_width, cost_per_ab) VALUES
  (1, 'Betonmischer 1000 l (Demo)', 1, 19.8),
  (1, 'Mähwerk (Demo)', 3, 5);
INSERT INTO persons (name, hourly_rate) VALUES ('Daniel (Demo)', 25);
INSERT INTO gespanne (base_id, name, tractor_id, load_level_id) VALUES (1, 'Betonmischen (Demo)', 1, 1);
INSERT INTO gespann_machines (gespann_id, machine_id) VALUES (1, 1);
INSERT INTO entries (neighbor_id, billing_year_id, entry_date, task_label, hours, hourly_rate, cost,
                     unit, quantity, unit_price, tractor_label, load_label, machine_labels, note, void_reason)
VALUES (1, 1, '2025-09-09', 'Betonmischen', 4, 55.8, 223.2, 'h', 4, 55.8, 'Demo-Traktor', 'mittel', 'Betonmischer 1000 l (Demo)', '', ''),
       (1, 1, '2025-09-09', 'Mannstunden Daniel (Demo)', 4, 25, 100, 'h', 4, 25, '', '', '', '', ''),
       (2, 1, '2025-09-10', 'Mähen', 2, 51, 102, 'h', 2, 51, 'Demo-Traktor', 'mittel', 'Mähwerk (Demo)', '', ''),
       (3, 1, '2025-09-10', 'Mähen', 2, 51, 102, 'h', 2, 51, 'Demo-Traktor', 'mittel', 'Mähwerk (Demo)', '', '');
UPDATE entries SET linked_entry_id = 1, person_id = 1 WHERE id = 2;
INSERT INTO payments (billing_year_id, neighbor_id, amount, paid_on, note)
VALUES (1, 1, 50, '2025-09-12', 'Demo-Anzahlung');
INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date)
VALUES (1, 1, -80.8, 'Demo-Gegenleistung', '2025-09-15');
-- Legacy issued fixture makes the reminder template visible without sending anything.
INSERT INTO invoices (billing_year_id, neighbor_id, number, issued_on)
VALUES (1, 3, '2025-0001', '2025-09-12');
