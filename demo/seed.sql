-- Sample data for the practice instance (compose.demo.yml). It runs only against
-- the dedicated treckrr_demo database and only while that database is still
-- empty, so a second run is a harmless no-op. Every name, address and amount is
-- invented; IBAN and addresses are the usual documentation examples.
DO $$
DECLARE
	v_base BIGINT; v_year BIGINT;
	v_leicht BIGINT; v_mittel BIGINT; v_schwer BIGINT;
	v_t1 BIGINT; v_t2 BIGINT; v_t3 BIGINT;
	v_maeh BIGINT; v_heuer BIGINT; v_schwader BIGINT; v_kipper BIGINT;
	v_gruber BIGINT; v_moser BIGINT; v_huber BIGINT; v_leitner BIGINT;
	v_entry BIGINT;
	r RECORD;
BEGIN
	IF current_database() <> 'treckrr_demo' THEN
		RAISE EXCEPTION 'refusing to seed database %: demo data belongs only in treckrr_demo', current_database();
	END IF;
	IF EXISTS (SELECT 1 FROM price_bases) THEN
		RAISE NOTICE 'treckrr_demo already holds data; seed skipped';
		RETURN;
	END IF;

	UPDATE users SET must_change_password = false WHERE username = 'admin';

	INSERT INTO company (id, name, address, iban, payment_term_days)
	VALUES (1, 'Übungshof Beispiel', E'Musterweg 1\n4710 Beispieldorf', 'AT611904300234573201', 14)
	ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, address = EXCLUDED.address,
		iban = EXCLUDED.iban, payment_term_days = EXCLUDED.payment_term_days;

	INSERT INTO price_bases (year, name) VALUES (2026, 'Übungsgrundlage 2026') RETURNING id INTO v_base;
	INSERT INTO billing_years (year, base_id, label, status) VALUES (2026, v_base, '2026', 'in_progress') RETURNING id INTO v_year;

	INSERT INTO load_levels (base_id, name, cost_per_ps, sort_order) VALUES (v_base, 'leicht', 0.33, 1) RETURNING id INTO v_leicht;
	INSERT INTO load_levels (base_id, name, cost_per_ps, sort_order) VALUES (v_base, 'mittel', 0.36, 2) RETURNING id INTO v_mittel;
	INSERT INTO load_levels (base_id, name, cost_per_ps, sort_order) VALUES (v_base, 'schwer', 0.38, 3) RETURNING id INTO v_schwer;

	INSERT INTO tractors (base_id, ident, name, ps, sort_order) VALUES (v_base, 'T1', 'Allrad 120', 120, 1) RETURNING id INTO v_t1;
	INSERT INTO tractors (base_id, ident, name, ps, sort_order) VALUES (v_base, 'T2', 'Allrad 98', 98, 2) RETURNING id INTO v_t2;
	INSERT INTO tractors (base_id, ident, name, ps, sort_order) VALUES (v_base, 'T3', 'Kompakt 65', 65, 3) RETURNING id INTO v_t3;

	INSERT INTO machines (base_id, name, working_width, cost_per_ab, sort_order, category, self_cost_per_h)
		VALUES (v_base, 'Scheibenmähwerk 3 m', 3, 6, 1, 'Grünland', 11.5) RETURNING id INTO v_maeh;
	INSERT INTO machines (base_id, name, working_width, cost_per_ab, sort_order, category, self_cost_per_h)
		VALUES (v_base, 'Kreiselheuer 6,5 m', 6.5, 2, 2, 'Grünland', 7.8) RETURNING id INTO v_heuer;
	INSERT INTO machines (base_id, name, working_width, cost_per_ab, sort_order, category, self_cost_per_h)
		VALUES (v_base, 'Kreiselschwader 4,2 m', 4.2, 2.5, 3, 'Grünland', 6.2) RETURNING id INTO v_schwader;
	INSERT INTO machines (base_id, name, working_width, cost_per_ab, sort_order, category, self_cost_per_h)
		VALUES (v_base, 'Dreiseitenkipper', 1, 12, 4, 'Transport', 7.5) RETURNING id INTO v_kipper;

	WITH g AS (
		INSERT INTO gespanne (base_id, name, tractor_id, load_level_id, sort_order)
		VALUES (v_base, 'Mähen 3 m', v_t1, v_mittel, 1) RETURNING id
	) INSERT INTO gespann_machines (gespann_id, machine_id) SELECT id, v_maeh FROM g;

	INSERT INTO persons (name, hourly_rate) VALUES ('Sepp (Aushilfe)', 18);

	INSERT INTO neighbors (name, address, email) VALUES ('Hof Gruber', E'Wiesenweg 4\n4710 Beispieldorf', 'gruber@example.invalid') RETURNING id INTO v_gruber;
	INSERT INTO neighbors (name, address, email) VALUES ('Moser Anton', E'Am Bach 12\n4710 Beispieldorf', 'moser@example.invalid') RETURNING id INTO v_moser;
	INSERT INTO neighbors (name, address, email) VALUES ('Huber KG', E'Feldstraße 7\n4711 Nachbarort', '') RETURNING id INTO v_huber;
	INSERT INTO neighbors (name, address, email) VALUES ('Leitner Maria', E'Obere Zeile 3\n4711 Nachbarort', 'leitner@example.invalid') RETURNING id INTO v_leitner;
	INSERT INTO billing_year_neighbors (billing_year_id, neighbor_id)
		SELECT v_year, unnest(ARRAY[v_gruber, v_moser, v_huber, v_leitner]);

	-- Rates follow the cost model: tractor PS × load level + Σ machine width × rate.
	FOR r IN SELECT * FROM (VALUES
		(v_gruber,  DATE '2026-05-12', 'Mähen',     v_t1, v_mittel, 'Allrad 120', 'mittel', v_maeh,     'Scheibenmähwerk 3 m',   18.00::numeric, 11.5::numeric, 3.5::numeric, 61.20::numeric),
		(v_gruber,  DATE '2026-05-14', 'Heuen',     v_t2, v_leicht, 'Allrad 98',  'leicht', v_heuer,    'Kreiselheuer 6,5 m',    13.00, 7.8, 2.5, 45.34),
		(v_moser,   DATE '2026-06-03', 'Schwaden',  v_t2, v_leicht, 'Allrad 98',  'leicht', v_schwader, 'Kreiselschwader 4,2 m', 10.50, 6.2, 2.0, 42.84),
		(v_moser,   DATE '2026-07-20', 'Transport', v_t1, v_schwer, 'Allrad 120', 'schwer', v_kipper,   'Dreiseitenkipper',      12.00, 7.5, 1.5, 57.60),
		(v_huber,   DATE '2026-08-02', 'Mähen',     v_t1, v_mittel, 'Allrad 120', 'mittel', v_maeh,     'Scheibenmähwerk 3 m',   18.00, 11.5, 4.0, 61.20),
		(v_huber,   DATE '2026-09-10', 'Transport', v_t1, v_schwer, 'Allrad 120', 'schwer', v_kipper,   'Dreiseitenkipper',      12.00, 7.5, 2.0, 57.60),
		(v_leitner, DATE '2026-06-18', 'Heuen',     v_t2, v_leicht, 'Allrad 98',  'leicht', v_heuer,    'Kreiselheuer 6,5 m',    13.00, 7.8, 3.0, 45.34),
		(v_leitner, DATE '2026-06-19', 'Schwaden',  v_t2, v_leicht, 'Allrad 98',  'leicht', v_schwader, 'Kreiselschwader 4,2 m', 10.50, 6.2, 1.5, 42.84)
	) AS t(neighbor, day, task, tractor, load, tractor_label, load_label, machine, machine_label, machine_rate, self_cost, hours, rate)
	LOOP
		INSERT INTO entries (neighbor_id, billing_year_id, entry_date, task_label, tractor_id, load_level_id,
			tractor_label, load_label, machine_labels, hours, hourly_rate, cost, unit, quantity, unit_price)
		VALUES (r.neighbor, v_year, r.day, r.task, r.tractor, r.load, r.tractor_label, r.load_label,
			r.machine_label, r.hours, r.rate, round(r.hours * r.rate, 2), 'h', r.hours, r.rate)
		RETURNING id INTO v_entry;
		INSERT INTO entry_machine_snapshots (entry_id, machine_id, machine_label, hourly_rate, self_cost_per_h)
		VALUES (v_entry, r.machine, r.machine_label, r.machine_rate, r.self_cost);
	END LOOP;

	INSERT INTO payments (billing_year_id, neighbor_id, amount, paid_on, note, method)
		VALUES (v_year, v_gruber, 100, DATE '2026-06-01', 'Anzahlung', 'bank');
	INSERT INTO payments (billing_year_id, neighbor_id, amount, paid_on, note, method)
		VALUES (v_year, v_moser, 85.68, DATE '2026-06-30', '', 'bar');
END $$;
