// Live rate/cost preview for the booking form. Mirrors internal/calc.
(function () {
	"use strict";

	var form = document.querySelector("[data-entry-form]");
	if (!form) return;
	var unified = form.treckrrBooking || null;

	var round2 = function (n) { return Math.round(n * 100) / 100; };
	var fmt = function (n) {
		return n.toLocaleString("de-DE", { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + " €";
	};

	var pricing = null;
	var byId = function (list, id) {
		for (var i = 0; i < list.length; i++) { if (list[i].id === id) return list[i]; }
		return null;
	};

	var panels = {
		gespann: form.querySelector('[data-mode-panel="gespann"]'),
		manual: form.querySelector('[data-mode-panel="manual"]')
	};
	var rateEl = form.querySelector("[data-rate]");
	var costEl = form.querySelector("[data-cost]");
	var hoursEl = form.querySelector("[data-hours]");
	var unitEl = form.querySelector("[data-unit]");
	var hOnly = form.querySelector("[data-h-only]");
	var qtyOnly = form.querySelector("[data-qty-only]");
	var qtyEl = form.querySelector("[data-qty]");
	var unitPriceEl = form.querySelector("[data-unit-price]");
	var qtyCostEl = form.querySelector("[data-qty-cost]");
	var unitLabels = form.querySelectorAll("[data-unit-label]");
	var unitCustomWrap = form.querySelector("[data-unit-custom]");
	var unitCustomInput = form.querySelector("[data-unit-custom-input]");
	var noTractorNote = form.querySelector("[data-no-tractor-note]");

	function decVal(el) { return parseFloat((((el && el.value) || "0")).replace(",", ".")) || 0; }
	function isHours() {
		var u = ((unitEl && unitEl.value) || "").trim();
		return u === "" || u === "h";
	}
	// The effective unit label (resolves the "Andere Einheit" option to its text).
	function unitValue() {
		var v = ((unitEl && unitEl.value) || "").trim();
		if (v === "__custom") return ((unitCustomInput && unitCustomInput.value) || "").trim();
		return v;
	}

	function currentMode() {
		var select = form.querySelector('select[name="mode"]');
		if (select) return select.value || "gespann";
		var checked = form.querySelector("[data-mode-toggle]:checked");
		return checked ? checked.value : "gespann";
	}

	// One visible choice drives the original successful controls. Keeping their
	// names and change events intact preserves capture, offline and POST contracts.
	var billingSelect = null;
	if (!unified && unitEl && unitEl.tagName === "SELECT" && form.querySelector("[data-mode-toggle]")) {
		var unitField = unitEl.closest(".field");
		var modeGroup = form.querySelector("[data-mode-toggle]").closest(".segmented");
		if (unitField && modeGroup) {
			var billingField = document.createElement("label");
			billingField.className = "field";
			var billingLabel = document.createElement("span");
			billingLabel.textContent = "Abrechnung";
			billingSelect = document.createElement("select");
			billingSelect.className = "select";
			billingSelect.setAttribute("data-billing-select", "");
			billingSelect.add(new Option("Stunden · fixes Gespann", "h:gespann"));
			billingSelect.add(new Option("Stunden · freie Zusammenstellung", "h:manual"));
			Array.from(unitEl.options).forEach(function (option) {
				if (option.value !== "h") billingSelect.add(new Option(option.textContent, option.value));
			});
			billingField.append(billingLabel, billingSelect);
			unitField.before(billingField);
			unitField.hidden = true;
			modeGroup.hidden = true;
			billingSelect.addEventListener("change", function () {
				var parts = billingSelect.value.split(":");
				unitEl.value = parts[0];
				if (parts[1]) {
					var mode = form.querySelector('[data-mode-toggle][value="' + parts[1] + '"]');
					mode.checked = true;
					mode.dispatchEvent(new Event("change", { bubbles: true }));
				}
				unitEl.dispatchEvent(new Event("change", { bubbles: true }));
			});
		}
	}

	/** Reflects canonical billing controls and makes any additional person charge visible. */
	function syncBillingChoice() {
		if (unified) { unified.update(); return; }
		if (billingSelect) billingSelect.value = isHours() ? "h:" + currentMode() : unitEl.value;
		var person = form.querySelector('select[name="person_id"]');
		var summary = form.querySelector("[data-person-details] > summary .small");
		if (person && summary) summary.textContent = person.value ? person.selectedOptions[0].textContent : "optional";
	}

	function machineRates(ids) {
		if (!pricing) return 0;
		var sum = 0;
		ids.forEach(function (id) {
			var m = byId(pricing.machines, id);
			if (m) sum += m.rate;
		});
		return sum;
	}

	// Returns { ps, loadCost, machineIds, hasTractor } or null when the selection
	// cannot be priced. A rig without a tractor is priceable as long as it carries
	// a machine — the customer's own tractor pulls it — and mirrors the same
	// all-or-nothing rule the server applies in calc.GespannRate: half a tractor
	// pair has no rate, so it stays unpriced here too.
	function resolveSelection() {
		if (!pricing) return null;
		var ids = [];
		if (currentMode() === "gespann") {
			var gid = parseInt(form.querySelector("[data-gespann-select]").value, 10);
			if (!gid) return null;
			var g = byId(pricing.gespanne, gid);
			if (!g) return null;
			ids = g.machines || [];
			if (!g.tractor && !g.load) {
				if (!ids.length) return null;
				return { ps: 0, loadCost: 0, machineIds: ids, hasTractor: false };
			}
			if (!g.tractor || !g.load) return null;
			var t = byId(pricing.tractors, g.tractor);
			var l = byId(pricing.loads, g.load);
			if (!t || !l) return null;
			return { ps: t.ps, loadCost: l.cost, machineIds: ids, hasTractor: true };
		}
		form.querySelectorAll("[data-machine]:checked").forEach(function (c) {
			ids.push(parseInt(c.value, 10));
		});
		var tid = parseInt(form.querySelector("[data-tractor-select]").value, 10);
		var lid = parseInt(form.querySelector("[data-load-select]").value, 10);
		if (!tid && !lid) {
			if (!ids.length) return null;
			return { ps: 0, loadCost: 0, machineIds: ids, hasTractor: false };
		}
		if (!tid || !lid) return null;
		var tr = byId(pricing.tractors, tid);
		var lo = byId(pricing.loads, lid);
		if (!tr || !lo) return null;
		return { ps: tr.ps, loadCost: lo.cost, machineIds: ids, hasTractor: true };
	}

	function update() {
		if (unified && !unified.standard()) { unified.update(); return; }
		if (!isHours()) {
			var q = decVal(qtyEl), p = decVal(unitPriceEl);
			if (qtyCostEl) qtyCostEl.textContent = (q > 0 && p > 0) ? fmt(round2(q * p)) : "–";
			if (noTractorNote) noTractorNote.hidden = true;
			if (unified) unified.update();
			return;
		}
		var sel = resolveSelection();
		if (noTractorNote) noTractorNote.hidden = !(sel && sel.hasTractor === false);
		if (!sel) {
			if (unified) unified.update(null, null);
			rateEl.textContent = "–";
			costEl.textContent = "–";
			return;
		}
		var rate = round2(round2(sel.ps * sel.loadCost) + machineRates(sel.machineIds));
		rateEl.textContent = fmt(rate) + " / h";
		var hours = parseFloat((hoursEl.value || "0").replace(",", "."));
		costEl.textContent = hours > 0 ? fmt(round2(hours * rate)) : "–";
		if (unified) unified.update(hours > 0 ? round2(hours * rate) : null, rate);
	}

	function applyMode() {
		if (unified) { unified.refreshVisibility(); update(); return; }
		var mode = currentMode();
		if (panels.gespann) panels.gespann.hidden = mode !== "gespann";
		if (panels.manual) panels.manual.hidden = mode !== "manual";
		update();
	}

	// Toggle the hour path (rig + rate) vs the quantity path (menge × unit price)
	// based on the chosen unit; hidden fields are barred from HTML validation.
	function applyUnit() {
		if (unified) { unified.refreshVisibility(); update(); return; }
		if (unitCustomWrap) unitCustomWrap.hidden = !(unitEl && unitEl.value === "__custom");
		var hours = isHours();
		if (hOnly) hOnly.hidden = !hours;
		if (qtyOnly) qtyOnly.hidden = hours;
		// A hidden select still submits: a person picked on the hours path would
		// ride along into a quantity booking the user can no longer see it on.
		if (!hours) {
			var personSel = form.querySelector('select[name="person_id"]');
			if (personSel) personSel.value = "";
		}
		// The hours field is `required`, but on the quantity path it's inside the
		// display:none panel; a required non-focusable field makes Chromium abort the
		// submit silently ("invalid form control is not focusable"). Only require it
		// while it's the visible path so a quantity booking can actually be saved.
		if (hoursEl) hoursEl.required = hours;
		var label = unitValue() || "Einheit";
		for (var i = 0; i < unitLabels.length; i++) unitLabels[i].textContent = label;
		update();
	}

	form.addEventListener("input", function (e) {
		if (e.target.matches("[data-unit], [data-unit-custom-input]")) applyUnit();
		else update();
	});
	form.addEventListener("change", function (e) {
		if (e.target.matches("[data-mode-toggle], [data-mode-select]")) applyMode();
		else if (e.target.matches("[data-unit], [data-unit-custom-input]")) applyUnit();
		else update();
		syncBillingChoice();
	});
	form.addEventListener("reset", function () {
		setTimeout(function () { applyMode(); applyUnit(); syncBillingChoice(); }, 0);
	});
	window.addEventListener("pageshow", syncBillingChoice);
	syncBillingChoice();

	var previews = form.querySelectorAll("[data-rate-preview]");
	function setLoading(on) { for (var i = 0; i < previews.length; i++) previews[i].classList.toggle("is-loading", on); }
	setLoading(true);
	fetch(form.getAttribute("data-pricing-url"), { credentials: "same-origin" })
		.then(function (r) { if (!r.ok) throw new Error("pricing"); return r.json(); })
		.then(function (data) {
			pricing = {
				tractors: data.tractors || [],
				loads: data.loads || [],
				machines: data.machines || [],
				gespanne: data.gespanne || []
			};
			applyMode();
		})
		.catch(function () {
			var status = form.querySelector("[data-pricing-status]");
			if (status) { status.hidden = false; status.textContent = "Maschinenpreis-Vorschau nicht verfügbar. Beim Speichern wird der Preis aus der Grundlage berechnet."; }
		})
		.then(function () { setLoading(false); });

	// Pre-save guard: warn (never block) on implausible hours or a same-day
	// duplicate of the same task. Keeps the form intact — a cancelled confirm just
	// stays on the page. requestSubmit() re-fires this handler with the flag set.
	form.addEventListener("submit", function (e) {
		if (unified && !unified.standard()) return;
		if (form.dataset.checked === "1") return;              // re-submit after the precheck → let it through
		if (form.dataset.checking === "1") { e.preventDefault(); return; } // a precheck is already in flight
		var q = form.querySelector('[name="year_id"]'), n = form.querySelector('[name="neighbor_id"]');
		if (!n || !q) return; // edit form (no ids) → no duplicate check
		e.preventDefault();
		form.dataset.checking = "1";
		var params = new URLSearchParams({
			neighbor_id: n.value, year_id: q.value,
			entry_date: (form.querySelector('[name="entry_date"]') || {}).value || "",
			task_label: (form.querySelector('[name="task_label"]') || {}).value || "",
			// On the quantity path the hidden hours field may still hold a stale value;
			// send "0" so the >24h plausibility check doesn't fire on a unit booking.
			hours: (isHours() && hoursEl && hoursEl.value) || "0"
		});
		function go() {
			form.dataset.checked = "1";
			// The first (prevented) submit already tripped app.js's double-submit
			// guard (dataset.submitting="1"); clear it so this programmatic re-submit
			// isn't mistaken for a duplicate click and blocked — which would leave the
			// button spinning forever and never POST the booking.
			form.dataset.submitting = "";
			form.requestSubmit();
		}
		fetch("/api/entries/precheck?" + params.toString(), { credentials: "same-origin" })
			.then(function (r) { return r.ok ? r.json() : {}; })
			.then(function (d) { if (!d.warn || window.confirm(d.warn)) go(); else releaseButton(); })
			.catch(function () { go(); }); // fail-open: never block saving on a network hiccup
	});

	// When the user cancels the plausibility confirm, the booking is NOT submitted,
	// so undo app.js's spinner/disabled state and let them edit and retry.
	function releaseButton() {
		form.dataset.checking = "";
		form.dataset.submitting = "";
		var b = form.querySelector("button.btn--primary[type='submit'], button[type='submit'].btn--primary");
		if (b) { b.classList.remove("is-submitting"); b.removeAttribute("aria-busy"); b.disabled = false; }
	}

	// Ctrl/Cmd+Enter saves the booking from anywhere in the form. Routes through
	// requestSubmit() so the normal submit path (plausibility precheck + double-submit
	// guard) runs exactly as a click on "Buchung speichern" would — never a raw submit.
	form.addEventListener("keydown", function (e) {
		if ((e.ctrlKey || e.metaKey) && (e.key === "Enter" || e.keyCode === 13)) {
			e.preventDefault();
			if (form.dataset.submitting === "1" || form.dataset.checking === "1") return; // a save is already in flight
			// requestSubmit() so the plausibility precheck + double-submit guard run,
			// exactly as a click would. NO form.submit() fallback: a raw submit would
			// skip those handlers (the thing this shortcut must never do). requestSubmit
			// is supported by every browser since ~2020; on an older one Ctrl+Enter is a
			// no-op and the user clicks the button instead.
			if (typeof form.requestSubmit === "function") form.requestSubmit();
		}
	});

	applyMode();
	applyUnit();
})();
