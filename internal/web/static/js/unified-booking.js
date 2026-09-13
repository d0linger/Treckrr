// Progressive field grouping only: the existing entry-form/offline submit
// handlers remain the single submission path for every booking type.
(function () {
	"use strict";
	var form = document.querySelector("[data-unified-booking]");
	if (!form) return;
	var kind = form.querySelector("[name=booking_kind]");
	var unit = form.querySelector("[name=unit]");
	var preview = form.querySelector("[data-booking-preview]");
	var drafts = Object.create(null);
	var snapshotFields = Array.from(form.querySelectorAll("input[name], select[name]")).filter(function (field) {
		return ["booking_kind", "booking_direction", "entry_date", "year_id", "neighbor_id", "csrf_token", "idempotency_key"].indexOf(field.name) === -1;
	});
	var defaults = snapshotFields.map(function (field) {
		var option = field.tagName === "SELECT" && Array.from(field.options).find(function (item) { return item.defaultSelected; });
		return { value: field.tagName === "SELECT" ? (option || field.options[0]).value : field.defaultValue, checked: field.defaultChecked };
	});
	var lastKey = "";
	var machineCost = null;
	var machineRate = null;
	var fmt = function (n) { return n.toLocaleString("de-DE", { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + " €"; };
	var round = function (n) { return Math.round((n + Number.EPSILON) * 100) / 100; };
	var field = function (name) { return form.querySelector('[name="' + name + '"]'); };
	var value = function (name) { return (field(name) && field(name).value || "").trim(); };
	var number = function (name) { return Number(value(name).replace(",", ".")); };
	var direction = function () { return form.querySelector("[name=booking_direction]:checked").value; };
	var key = function () { return kind.value + ":" + direction(); };
	var ownEquipment = function () { return key() === "equipment:out"; };
	var standard = function () { return direction() === "out" && (kind.value === "equipment" || kind.value === "quantity"); };
	var ownPersonRate = function () {
		var selected = field("person_id").selectedOptions[0];
		return value("person_rate") ? number("person_rate") : Number(selected && selected.dataset.personRate || 0);
	};
	var positive = function (n) { return Number.isFinite(n) && n > 0; };

	/** Keeps separate in-memory drafts so switching party or type cannot reuse a hidden price. */
	function selectDraft() {
		if (lastKey) drafts[lastKey] = snapshotFields.map(function (el) { return { value: el.value, checked: el.checked }; });
		if (lastKey && lastKey !== key()) {
			(snapshotFields).forEach(function (el, index) {
				var saved = (drafts[key()] || defaults)[index];
				el.value = saved.value;
				if (el.type === "checkbox" || el.type === "radio") el.checked = saved.checked;
			});
			machineCost = null;
			machineRate = null;
		}
		lastKey = key();
		if (kind.value === "quantity") { if (unit.value === "h") unit.value = "ha"; }
		else unit.value = "h";
		refreshVisibility();
	}

	/** Makes only active controls successful; native no-JS forms still use server-side validation. */
	function refreshVisibility() {
		form.querySelectorAll("[data-booking-panel]").forEach(function (panel) {
			panel.hidden = panel.dataset.bookingPanel.split(" ").indexOf(key()) === -1;
		});
		var mode = form.querySelector("[data-mode-toggle]:checked");
		form.querySelectorAll("[data-mode-panel]").forEach(function (panel) { panel.hidden = !ownEquipment() || panel.dataset.modePanel !== (mode && mode.value); });
		form.querySelector("[data-unit-custom]").hidden = kind.value !== "quantity" || unit.value !== "__custom";
		form.querySelectorAll("input, select").forEach(function (el) {
			// Only inactive service branches stop contributing controls. The machine
			// search hides labels for presentation; their checked IDs still belong
			// to the selected rig and must reach both online and offline submissions.
			if (el.closest("[data-booking-panel]")) {
				el.disabled = !!el.closest("[data-booking-panel][hidden], [data-mode-panel][hidden], [data-unit-custom][hidden]");
			}
			if (el.hasAttribute("data-booking-required")) el.required = !el.disabled;
		});
		field("unit_custom").required = kind.value === "quantity" && unit.value === "__custom";
		var labor = kind.value === "labor";
		field("person_id").required = labor && direction() === "out";
		var hasOwnPerson = !field("person_id").disabled && !!value("person_id");
		field("person_rate").disabled = !hasOwnPerson;
		field("person_hours").disabled = !hasOwnPerson || !ownEquipment();
		var partner = direction() === "in" && (kind.value === "equipment" || labor);
		var hasPartner = partner && (labor || !!value("partner_person") || !!value("partner_person_rate"));
		field("partner_person").required = hasPartner;
		field("partner_person_rate").required = hasPartner;
		field("partner_person_hours").disabled = !hasPartner || labor;
		form.querySelector("[data-person-heading]").textContent = labor ? "Person / Stundensatz" : "Person mitbuchen";
		form.querySelector("[data-partner-person-heading]").textContent = labor ? "Person / vereinbarter Stundensatz" : "Person des Nachbarn mitbuchen";
		if (labor && direction() === "out") form.querySelector("[data-person-details]").open = true;
		if (labor && direction() === "in") form.querySelector("[data-partner-person-details]").open = true;
		form.querySelector("[data-person-help]").textContent = labor ? "Reine Arbeitszeit ohne Traktor oder Maschinen." : "Zusätzliche Arbeitszeit wird als verknüpfte eigene Position ausgewiesen. Leer bei Mannstunden bedeutet dieselben Stunden wie die Maschine.";
		form.querySelector("[data-booking-hours-label]").textContent = labor ? "Mannstunden" : "Stunden";
		form.querySelector("[data-booking-own-rate]").hidden = !ownEquipment();
		form.querySelector("[data-booking-direction-note]").textContent = direction() === "out" ? "Meine Leistung erhöht die Forderung an den Nachbarn." : "Gegenleistung: Ich schulde dem Nachbarn diesen Betrag. Eigene Leistungen bleiben unverändert.";
		form.querySelectorAll("[data-unit-label]").forEach(function (label) { label.textContent = unit.value === "__custom" ? value("unit_custom") || "Einheit" : unit.value; });
	}

	/** Renders an itemized, signed settlement preview without ever accepting a client total on the server. */
	function updatePreview(cost, rate) {
		if (arguments.length) { machineCost = cost; machineRate = rate; }
		var lines = [];
		var hours = number("hours");
		var total = null;
		var add = function (label, quantity, price) {
			if (!positive(quantity) || !positive(price)) return false;
			var amount = round(quantity * price);
			lines.push({ label: label, quantity: quantity, price: price, amount: amount });
			total = round((total || 0) + amount);
			return true;
		};
		var valid = true;
		if (kind.value === "fixed") valid = add("Freie Position", 1, number("amount"));
		else if (kind.value === "quantity") valid = add("Mengenleistung · " + (unit.value === "__custom" ? value("unit_custom") : unit.value), number("quantity"), number("unit_price"));
		else if (kind.value === "labor") valid = add("Mannstunden", hours, direction() === "out" ? ownPersonRate() : number("partner_person_rate"));
		else if (direction() === "in") valid = add(value("partner_label") || "Gespann des Nachbarn", hours, number("partner_rate"));
		else {
			valid = positive(machineCost) && positive(machineRate) && positive(hours);
			if (valid) { total = machineCost; lines.push({ label: "Maschinenleistung", quantity: hours, price: machineRate, amount: machineCost }); }
		}
		if (kind.value === "equipment" && direction() === "out" && value("person_id")) valid = add("Mannstunden zusätzlich", value("person_hours") ? number("person_hours") : hours, ownPersonRate()) && valid;
		if (kind.value === "equipment" && direction() === "in" && (value("partner_person") || value("partner_person_rate"))) valid = add("Mannstunden · " + value("partner_person"), value("partner_person_hours") ? number("partner_person_hours") : hours, number("partner_person_rate")) && valid;
		var summary = form.querySelector(direction() === "out" ? "[data-person-summary]" : "[data-partner-person-summary]");
		var personChosen = direction() === "out" ? !!value("person_id") : !!value("partner_person");
		if (summary) summary.textContent = personChosen ? (valid && lines.length ? fmt(lines[lines.length - 1].amount) : "Eingaben prüfen") : laborRequiredText();
		preview.replaceChildren();
		if (!valid || total === null) {
			var hint = document.createElement("p"); hint.className = "muted small";
			hint.textContent = "Leistung auswählen und positive Stunden oder Beträge eingeben. Die endgültige Berechnung erfolgt beim Speichern.";
			preview.appendChild(hint); return;
		}
		lines.forEach(function (line) {
			var row = document.createElement("div"); row.className = "booking-preview__line";
			var label = document.createElement("span"); label.textContent = line.label + " · " + line.quantity.toLocaleString("de-DE") + " × " + fmt(line.price);
			var amount = document.createElement("strong"); amount.textContent = fmt(line.amount);
			row.append(label, amount); preview.appendChild(row);
		});
		var result = document.createElement("div"); result.className = "booking-preview__total";
		var caption = document.createElement("span"); caption.textContent = direction() === "out" ? "Der Nachbar schuldet mir zusätzlich" : "Ich schulde dem Nachbarn zusätzlich";
		var sum = document.createElement("strong"); sum.textContent = fmt(total); result.append(caption, sum); preview.appendChild(result);
		if (form.hasAttribute("data-balance")) {
			var balance = round(Number(form.dataset.balance) + (direction() === "out" ? total : -total));
			var rest = document.createElement("p"); rest.className = "muted small";
			rest.textContent = "Rechnerischer Rest nach dieser Buchung: " + (balance === 0 ? "ausgeglichen" : fmt(Math.abs(balance)) + (balance > 0 ? " zu meinen Gunsten" : " zu Gunsten des Nachbarn"));
			preview.appendChild(rest);
		}
	}
	function laborRequiredText() { return kind.value === "labor" ? "erforderlich" : "optional"; }
	form.addEventListener("change", function (event) {
		if (event.target === kind || event.target.name === "booking_direction") selectDraft();
		else refreshVisibility();
		updatePreview();
	});
	form.addEventListener("input", function () { refreshVisibility(); updatePreview(); });
	form.addEventListener("reset", function () { setTimeout(function () { drafts = Object.create(null); lastKey = ""; selectDraft(); updatePreview(); }, 0); });
	// capture.js may have restored an old quantity default before this enhancement loads.
	if (unit.value && unit.value !== "h") kind.value = "quantity";
	form.treckrrBooking = { refreshVisibility: refreshVisibility, update: updatePreview, standard: standard, ownEquipment: ownEquipment };
	selectDraft(); updatePreview();
})();
