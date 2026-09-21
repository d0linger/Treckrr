// Shared progressive editor. Existing CSRF, precheck and offline listeners own submission.
(function () {
	"use strict";
	var form = document.querySelector("[data-unified-booking]");
	if (!form) return;
	var field = function (name) { return form.querySelector('[name="' + name + '"]'); };
	var value = function (name) { return (field(name) && field(name).value || "").trim(); };
	var numeric = function (el) { return Number((el && el.value || "").replace(",", ".")); };
	var number = function (name) { return numeric(field(name)); };
	var kind = field("booking_kind"), unit = field("unit"), mode = field("mode");
	var rows = form.querySelector("[data-person-rows]"), details = form.querySelector("[data-person-details]");
	var preview = form.querySelector("[data-booking-preview]");
	var locked = form.hasAttribute("data-booking-locked");
	var drafts = Object.create(null), lastKey = "", machineCost = null, machineRate = null, serial = 0;
	var storagePrefix = "treckrr:booking-defaults:v2:" + value("neighbor_id") + ":";
	var remembered = ["mode", "gespann_id", "tractor_id", "load_level_id", "machine_ids", "unit", "unit_custom", "task_label"];
	var immutable = ["booking_kind", "booking_direction", "entry_date", "year_id", "neighbor_id", "csrf_token", "idempotency_key", "booking_form_version", "copy_mode"];
	var controls = Array.from(form.querySelectorAll("input[name], select[name]")).filter(function (el) { return !el.closest("[data-person-row]") && immutable.indexOf(el.name) === -1; });
	var direction = function () { var radio = form.querySelector("[name=booking_direction]:checked"); return radio ? radio.value : value("booking_direction"); };
	var key = function () { return kind.value + ":" + direction(); };
	var catalogEquipment = function () { return kind.value === "equipment" && mode.value !== "free"; };
	var standard = function () { return catalogEquipment() || key() === "quantity:out"; };
	var positive = function (n) { return Number.isFinite(n) && n > 0; };
	var round = function (n) { return Math.round((n + Number.EPSILON) * 100) / 100; };
	var fmt = function (n) { return n.toLocaleString("de-DE", { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + " €"; };
	var rowField = function (row, name) { return row.querySelector('[name="' + name + '"]'); };
	var personRows = function () { return Array.from(rows.querySelectorAll("[data-person-row]")); };
	var activeRows = function () { return personRows().filter(function (row) { return !row.hidden; }); };
	var populated = function (row) { return ["person_id", "person_name", "person_hours", "person_rate"].some(function (name) { return !!rowField(row, name).value.trim(); }); };
	var snapshot = function (list) { return list.map(function (el) { return { value: el.value, checked: el.checked }; }); };
	var restore = function (list, values) { list.forEach(function (el, i) { el.value = values[i].value; if (el.type === "radio" || el.type === "checkbox") el.checked = values[i].checked; }); };
	var defaults = snapshot(controls);

	/** Assigns distinct accessible names to every live person group. */
	function identifyRows() {
		activeRows().forEach(function (row, i) {
			var legend = row.querySelector("legend");
			if (!legend.id) legend.id = "booking-person-" + (++serial);
			legend.textContent = "Person " + (i + 1); row.setAttribute("aria-labelledby", legend.id);
		});
	}
	/** Reuses no-JS reserve rows before cloning another blank person. */
	function addPerson(focus) {
		if (activeRows().length >= 20) return;
		var row = personRows().find(function (item) { return item.hidden && !item.hasAttribute("data-person-existing"); });
		if (!row) { row = form.querySelector("[data-person-template]").content.firstElementChild.cloneNode(true); rows.appendChild(row); }
		row.hidden = false; identifyRows(); refreshVisibility(); updatePreview();
		if (focus) rowField(row, "person_id").focus();
	}
	/** Exact unsaved prices stay in memory only, isolated by direction and type. */
	function saveDraft() {
		return { controls: snapshot(controls), people: personRows().map(function (row) { return { node: row.cloneNode(true), values: snapshot(Array.from(row.querySelectorAll("input, select"))) }; }), open: details.open };
	}
	function restoreDraft(draft) {
		restore(controls, draft ? draft.controls : defaults); rows.replaceChildren();
		if (draft) draft.people.forEach(function (saved) { var row = saved.node.cloneNode(true); restore(Array.from(row.querySelectorAll("input, select")), saved.values); rows.appendChild(row); });
		else addPerson(false);
		details.open = draft ? draft.open : kind.value === "labor";
	}
	/** Only offered catalog choices, never rates, are restored for this exact context. */
	function restoreDefaults() {
		if (!form.hasAttribute("data-entry-defaults")) return;
		try {
			var saved = JSON.parse(localStorage.getItem(storagePrefix + key()) || "null"); if (!saved || typeof saved !== "object") return;
			controls.forEach(function (el) {
				if (remembered.indexOf(el.name) === -1 || !(el.name in saved)) return;
				if (el.type === "checkbox") { el.checked = Array.isArray(saved[el.name]) && saved[el.name].indexOf(el.value) !== -1; return; }
				if (typeof saved[el.name] !== "string") return;
				if (el.tagName === "SELECT" && !Array.from(el.options).some(function (o) { return o.value === saved[el.name]; })) return;
				el.value = saved[el.name];
			});
		} catch (_) { /* Private storage is optional. */ }
	}
	function rememberDefaults() {
		if (!form.hasAttribute("data-entry-defaults")) return;
		var saved = {};
		controls.forEach(function (el) {
			if (el.disabled || remembered.indexOf(el.name) === -1) return;
			if (el.type === "checkbox") { if (!saved[el.name]) saved[el.name] = []; if (el.checked) saved[el.name].push(el.value); }
			else saved[el.name] = el.value;
		});
		try { localStorage.setItem(storagePrefix + key(), JSON.stringify(saved)); } catch (_) { /* Private storage is optional. */ }
	}
	function selectDraft() {
		if (lastKey && lastKey !== key()) { drafts[lastKey] = saveDraft(); restoreDraft(drafts[key()]); if (!drafts[key()]) restoreDefaults(); machineCost = null; machineRate = null; }
		lastKey = key();
		if (kind.value === "quantity") { if (!unit.value || unit.value === "h") unit.value = "ha"; } else unit.value = "h";
		identifyRows(); refreshVisibility(); updatePreview();
	}
	/** Disables whole branches, never a single column of the aligned person arrays. */
	function refreshVisibility() {
		form.querySelectorAll("[data-booking-panel]").forEach(function (panel) { panel.hidden = panel.dataset.bookingPanel.split(" ").indexOf(key()) === -1; });
		form.querySelectorAll("[data-mode-panel]").forEach(function (panel) { panel.hidden = kind.value !== "equipment" || panel.dataset.modePanel !== mode.value; });
		form.querySelector("[data-unit-custom]").hidden = kind.value !== "quantity" || unit.value !== "__custom";
		form.querySelector("[data-agreed-equipment-rate]").hidden = kind.value !== "equipment" || mode.value !== "free";
		controls.forEach(function (el) {
			el.disabled = locked || !!el.closest("[data-booking-panel][hidden], [data-mode-panel][hidden], [data-unit-custom][hidden], [data-agreed-equipment-rate][hidden]");
			if (el.hasAttribute("data-booking-required")) el.required = !el.disabled;
		});
		field("unit_custom").required = !field("unit_custom").disabled && unit.value === "__custom";
		field("gespann_id").required = kind.value === "equipment" && mode.value === "gespann" && !locked;
		field("hours").required = kind.value === "equipment" && !locked;
		field("hours").step = catalogEquipment() ? "0.001" : "0.0001";
		field("hours").min = catalogEquipment() ? "0.001" : "0.0001";
		field("task_label").required = (kind.value === "quantity" || kind.value === "fixed") && !locked;
		form.querySelector("[data-booking-task-help]").textContent = kind.value === "equipment" ? "Optional – ohne Eingabe wird das gewählte Gespann oder Gefährt übernommen." : kind.value === "labor" ? "Optional – ohne Eingabe wird die gewählte Person übernommen." : "Bitte die Leistung oder Kostenposition beschreiben.";
		form.querySelector("[data-booking-hours-label]").textContent = kind.value === "labor" ? "Standard-Mannstunden (optional)" : "Stunden";
		form.querySelector("[data-booking-catalog-rate]").hidden = !catalogEquipment();
		form.querySelector("[data-person-heading]").textContent = kind.value === "labor" ? "Personen und Mannstunden" : "Personen mitbuchen";
		form.querySelector("[data-person-help]").textContent = (kind.value === "equipment" || kind.value === "labor" ? "Leere Mannstunden übernehmen die Stunden oben. " : "Mannstunden bitte je Person eingeben. ") + "Der Stammsatz wird übernommen und kann je Person geändert werden.";
		form.querySelector("[data-booking-direction-note]").textContent = direction() === "out" ? "Meine Leistung erhöht die Forderung an den Nachbarn." : "Gegenleistung: Ich schulde dem Nachbarn diesen Betrag. Eigene Leistungen bleiben unverändert.";
		var copy = field("copy_people"), enabled = !locked && (!copy || copy.checked), visible = activeRows();
		personRows().forEach(function (row) {
			var disabled = !enabled || row.hidden, firstLabor = kind.value === "labor" && visible[0] === row;
			var state = rowField(row, "person_state"), selected = rowField(row, "person_id"), name = rowField(row, "person_name"), hours = rowField(row, "person_hours"), rate = rowField(row, "person_rate");
			if (firstLabor) state.value = "active";
			var participating = !disabled && state.value === "active" && (populated(row) || firstLabor);
			row.querySelectorAll("input, select").forEach(function (el) { el.disabled = disabled; el.required = false; });
			name.required = participating && !selected.value;
			row.querySelector("[data-person-name-field]").hidden = !!selected.value;
			rate.required = participating && !selected.value;
			hours.placeholder = kind.value === "equipment" || kind.value === "labor" ? "Leer = Stunden oben" : "Eigene Mannstunden";
			hours.required = participating && (kind.value === "quantity" || kind.value === "fixed" || (kind.value === "labor" && !positive(number("hours"))));
			var useRate = row.querySelector("[data-person-use-rate]"); useRate.hidden = !selected.value; useRate.disabled = disabled || !positive(Number(selected.selectedOptions[0] && selected.selectedOptions[0].dataset.personRate));
			row.querySelector("[data-person-remove]").hidden = firstLabor || !!row.querySelector("select[name=person_state]"); row.querySelector("[data-person-remove]").disabled = disabled;
			row.classList.toggle("booking-person--inactive", state.value !== "active");
		});
		if (kind.value === "labor" && enabled) details.open = true;
		form.querySelector("[data-person-add]").disabled = !enabled || visible.length >= 20;
		form.querySelectorAll("[data-unit-label]").forEach(function (label) { label.textContent = unit.value === "__custom" ? value("unit_custom") || "Einheit" : unit.value; });
	}
	/** Mirrors per-person rounding without trusting a client-computed total on save. */
	function updatePreview(cost, rate) {
		if (arguments.length) { machineCost = cost; machineRate = rate; }
		var lines = [], total = 0, valid = true;
		var add = function (label, qty, price) { if (!positive(qty) || !positive(price)) { valid = false; return; } var amount = round(qty * price); total = round(total + amount); lines.push({ label: label, qty: qty, price: price, amount: amount }); };
		if (kind.value === "fixed") add("Freie Position", 1, number("amount"));
		else if (kind.value === "quantity") add("Mengenleistung · " + (unit.value === "__custom" ? value("unit_custom") : unit.value), number("quantity"), number("unit_price"));
		else if (kind.value === "equipment") { if (catalogEquipment()) { if (positive(machineCost) && positive(machineRate)) add("Maschinenleistung", number("hours"), machineRate); else valid = false; } else add(value("partner_label") || "Maschinenleistung", number("hours"), number("partner_rate")); }
		var count = 0;
		activeRows().forEach(function (row) {
			var selected = rowField(row, "person_id"); if (selected.disabled || rowField(row, "person_state").value !== "active" || !populated(row)) return;
			count++;
			var option = selected.selectedOptions[0], name = selected.value ? option.textContent : rowField(row, "person_name").value.trim();
			var rate = rowField(row, "person_rate").value ? numeric(rowField(row, "person_rate")) : selected.value ? Number(option.dataset.personRate) : 0;
			var hours = rowField(row, "person_hours").value ? numeric(rowField(row, "person_hours")) : kind.value === "equipment" || kind.value === "labor" ? number("hours") : 0;
			if (!name) valid = false; add("Mannstunden · " + name, hours, rate);
		});
		if (kind.value === "labor" && !count) valid = false;
		form.querySelector("[data-person-summary]").textContent = count ? count + (count === 1 ? " Person" : " Personen") : kind.value === "labor" ? "mindestens eine Person" : "optional";
		preview.replaceChildren();
		if (!valid || !lines.length) { var hint = document.createElement("p"); hint.className = "muted small"; hint.textContent = "Leistung auswählen und positive Stunden oder Beträge eingeben. Die endgültige Berechnung erfolgt beim Speichern."; preview.appendChild(hint); return; }
		lines.forEach(function (line) { var row = document.createElement("div"); row.className = "booking-preview__line"; var caption = document.createElement("span"); caption.textContent = line.label + " · " + line.qty.toLocaleString("de-DE", { maximumFractionDigits: 4 }) + " × " + fmt(line.price); var amount = document.createElement("strong"); amount.textContent = fmt(line.amount); row.append(caption, amount); preview.appendChild(row); });
		var result = document.createElement("div"); result.className = "booking-preview__total"; var caption = document.createElement("span"); caption.textContent = direction() === "out" ? "Der Nachbar schuldet mir zusätzlich" : "Ich schulde dem Nachbarn zusätzlich"; var sum = document.createElement("strong"); sum.textContent = fmt(total); result.append(caption, sum); preview.appendChild(result);
		if (form.hasAttribute("data-balance")) { var balance = round(Number(form.dataset.balance) + (direction() === "out" ? total : -total)); var rest = document.createElement("p"); rest.className = "muted small"; rest.textContent = "Rechnerischer Rest nach dieser Buchung: " + (balance === 0 ? "ausgeglichen" : fmt(Math.abs(balance)) + (balance > 0 ? " zu meinen Gunsten" : " zu Gunsten des Nachbarn")); preview.appendChild(rest); }
	}
	form.addEventListener("click", function (event) {
		var target = event.target.closest("button"); if (!target) return;
		if (target.hasAttribute("data-person-add")) { addPerson(true); form.querySelector("[data-person-status]").textContent = "Weitere Person hinzugefügt."; }
		var row = target.closest("[data-person-row]");
		if (row && target.hasAttribute("data-person-remove")) { var index = activeRows().indexOf(row); row.remove(); if (!activeRows().length) addPerson(false); identifyRows(); refreshVisibility(); updatePreview(); rowField(activeRows()[Math.min(index, activeRows().length - 1)], "person_id").focus(); form.querySelector("[data-person-status]").textContent = "Person aus dem Entwurf entfernt."; }
		if (row && target.hasAttribute("data-person-use-rate")) { rowField(row, "person_rate").value = rowField(row, "person_id").selectedOptions[0].dataset.personRate; rowField(row, "person_rate").dispatchEvent(new Event("input", { bubbles: true })); }
		if (target.hasAttribute("data-defaults-reset")) { try { Object.keys(localStorage).filter(function (key) { return key.indexOf(storagePrefix) === 0 || key === "treckrr:entry-defaults:" + value("neighbor_id"); }).forEach(function (key) { localStorage.removeItem(key); }); } catch (_) { /* Storage is optional. */ } target.textContent = "Vorgaben gelöscht"; }
	});
	form.addEventListener("change", function (event) {
		if (event.target === kind || event.target.name === "booking_direction") selectDraft();
		else { var row = event.target.closest("[data-person-row]"); if (row && event.target.name === "person_id") { var option = event.target.selectedOptions[0]; rowField(row, "person_name").value = event.target.value ? option.textContent.replace(/ \(archiviert\)$/, "") : ""; rowField(row, "person_rate").value = event.target.value && positive(Number(option.dataset.personRate)) ? option.dataset.personRate : ""; }
			refreshVisibility(); updatePreview(); }
		rememberDefaults();
	});
	form.addEventListener("input", function () { refreshVisibility(); updatePreview(); });
	form.addEventListener("reset", function () { setTimeout(function () { drafts = Object.create(null); lastKey = ""; selectDraft(); }, 0); });
	var existing = personRows().filter(function (row) { return row.hasAttribute("data-person-existing"); });
	personRows().filter(function (row) { return !row.hasAttribute("data-person-existing"); }).forEach(function (row, i) { row.hidden = existing.length > 0 || i > 0; });
	form.querySelector("[data-person-add]").hidden = false;
	restoreDefaults();
	form.treckrrBooking = { refreshVisibility: refreshVisibility, update: updatePreview, standard: standard, catalogEquipment: catalogEquipment };
	selectDraft();
})();
