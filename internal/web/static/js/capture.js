// Erfassungs-Hilfen (Ausbaukarte 62/65/66): dynamic quick-entry rows, date
// quick-picks and remembered form defaults.
//
// Deliberately its own file and strictly non-intercepting: it reads and writes
// field VALUES and never registers a submit handler. The booking form already
// carries several stacked submit listeners (precheck, offline capture), and a
// fourth one is exactly how "Buchung speichern" starts hanging.
(function () {
	"use strict";

	// ---- 62: Schnellerfassung, Zeilen nachlegen ---------------------------
	// The server accepts up to 100 rows; the markup shipped a fixed six.
	function initQuickRows() {
		var body = document.querySelector("[data-quick-rows]");
		var add = document.querySelector("[data-quick-add]");
		if (!body || !add) return;
		var MAX = 100;
		function labelRow(row, index) {
			var names = { q_date: "Datum", q_gespann: "Gespann", q_hours: "Stunden", q_person: "Person" };
			row.querySelectorAll("input, select").forEach(function (field) {
				if (names[field.name]) field.setAttribute("aria-label", names[field.name] + " · Zeile " + (index + 1));
			});
		}
		body.querySelectorAll("tr").forEach(labelRow);
		add.addEventListener("click", function () {
			var rows = body.querySelectorAll("tr");
			if (!rows.length || rows.length >= MAX) {
				if (rows.length >= MAX) add.disabled = true;
				return;
			}
			var clone = rows[rows.length - 1].cloneNode(true);
			// A clone carries the last row's typed values; only the date is worth
			// keeping, since the next line is usually the same day.
			clone.querySelectorAll("input").forEach(function (i) {
				if (i.type !== "date") i.value = "";
			});
			clone.querySelectorAll("select").forEach(function (sel) { sel.selectedIndex = 0; });
			labelRow(clone, rows.length);
			body.appendChild(clone);
			var first = clone.querySelector("input, select");
			if (first) first.focus();
		});
	}

	// ---- 65: Datums-Schnellwahl -------------------------------------------
	// Buttons carry data-days ("0" = heute, "1" = gestern, "7" = vor einer
	// Woche) and target the date input named by data-target within the form.
	function isoDaysAgo(days) {
		var d = new Date();
		d.setDate(d.getDate() - days);
		// Local date, not toISOString(): that shifts to UTC and can land a
		// morning booking on the previous day west of Greenwich.
		var m = String(d.getMonth() + 1).padStart(2, "0");
		var day = String(d.getDate()).padStart(2, "0");
		return d.getFullYear() + "-" + m + "-" + day;
	}
	function initQuickDates() {
		document.querySelectorAll("[data-days]").forEach(function (btn) {
			btn.addEventListener("click", function () {
				var form = btn.closest("form");
				if (!form) return;
				var name = btn.getAttribute("data-target") || "entry_date";
				form.querySelectorAll('[name="' + name + '"]').forEach(function (input) {
					input.value = isoDaysAgo(parseInt(btn.getAttribute("data-days"), 10) || 0);
					input.dispatchEvent(new Event("change", { bubbles: true }));
				});
			});
		});
	}

	// ---- 66: gemerkte Formular-Defaults ------------------------------------
	// Unit, rig and task usually repeat. Remembered per neighbor in
	// localStorage — a per-viewer convenience, never anything the server needs.
	var KEY = "treckrr:entry-defaults:";
	function storeKey(form) {
		var n = form.querySelector('[name="neighbor_id"]');
		return KEY + (n && n.value ? n.value : "global");
	}
	function readDefaults(form) {
		try {
			var raw = localStorage.getItem(storeKey(form));
			return raw ? JSON.parse(raw) : null;
		} catch (e) { return null; }
	}
	function writeDefaults(form, data) {
		try { localStorage.setItem(storeKey(form), JSON.stringify(data)); } catch (e) { /* private mode */ }
	}
	function initDefaults() {
		// Existing records and copies must retain their server-rendered values.
		// Only the explicit new-booking form may restore or write remembered defaults.
		var form = document.querySelector("[data-entry-form][data-entry-defaults]");
		if (!form) return;
		var fields = ["unit", "gespann_id", "task_label", "mode"];
		var unified = form.hasAttribute("data-unified-booking");
		/** Counterparty prices/tasks must never become a new own-service default. */
		function ownStandardSelection() {
			if (!unified) return true;
			var direction = form.querySelector('[name="booking_direction"]:checked');
			var kind = form.querySelector('[name="booking_kind"]');
			return direction && direction.value === "out" && kind && (kind.value === "equipment" || kind.value === "quantity");
		}

		var saved = readDefaults(form);
		// Accept historical own-service defaults but never restore a marked foreign,
		// labor or fixed draft into the default equipment/quantity entry screen.
		if (saved && unified && ((saved.booking_direction && saved.booking_direction !== "out") ||
			(saved.booking_kind && saved.booking_kind !== "equipment" && saved.booking_kind !== "quantity"))) saved = null;
		if (saved) {
			fields.forEach(function (name) {
				if (!(name in saved)) return;
				var els = form.querySelectorAll('[name="' + name + '"]');
				els.forEach(function (el) {
					if (el.type === "radio") {
						if (el.value === saved[name]) {
							el.checked = true;
							el.dispatchEvent(new Event("change", { bubbles: true }));
						}
						return;
					}
					// Only restore a value the field actually offers, so a removed
					// Gespann or a renamed unit falls back to the form's own default.
					if (el.tagName === "SELECT") {
						var ok = Array.prototype.some.call(el.options, function (o) { return o.value === saved[name]; });
						if (!ok) return;
					}
					el.value = saved[name];
					el.dispatchEvent(new Event("change", { bubbles: true }));
				});
			});
		}

		// Remember on change, not on submit: no submit handler goes near this form.
		form.addEventListener("change", function (e) {
			var name = e.target && e.target.name;
			if (unified) {
				if (fields.indexOf(name) === -1 && name !== "booking_kind" && name !== "booking_direction") return;
				// This listener loads before the progressive form controller. Read after
				// its draft switch, not the outgoing values of the previously active path.
				queueMicrotask(function () {
					if (!ownStandardSelection()) return;
					var data = readDefaults(form) || {};
					var kind = form.querySelector('[name="booking_kind"]').value;
					data.booking_kind = kind;
					data.booking_direction = "out";
					fields.forEach(function (key) {
						if (kind === "quantity" && (key === "mode" || key === "gespann_id")) return;
						var control = form.querySelector('[name="' + key + '"]' + (key === "mode" ? ':checked' : ""));
						if (control) data[key] = control.value;
					});
					writeDefaults(form, data);
				});
				return;
			}
			if (fields.indexOf(name) === -1) return;
			var data = readDefaults(form) || {};
			if (e.target.type === "radio") {
				if (!e.target.checked) return;
				data[name] = e.target.value;
			} else {
				data[name] = e.target.value;
			}
			writeDefaults(form, data);
		});

		var reset = document.querySelector("[data-defaults-reset]");
		if (reset) {
			reset.addEventListener("click", function () {
				try { localStorage.removeItem(storeKey(form)); } catch (e) { /* ignore */ }
				reset.textContent = "Vorgaben gelöscht";
				reset.disabled = true;
			});
		}
	}

	initQuickRows();
	initQuickDates();
	initDefaults();
})();
