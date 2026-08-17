// Live rate/cost preview for the booking form. Mirrors internal/calc.
(function () {
	"use strict";

	var form = document.querySelector("[data-entry-form]");
	if (!form) return;

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
		var checked = form.querySelector("[data-mode-toggle]:checked");
		return checked ? checked.value : "gespann";
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

	function resolveSelection() {
		// Returns { ps, loadCost, machineIds } or null when incomplete.
		if (!pricing) return null;
		if (currentMode() === "gespann") {
			var gid = parseInt(form.querySelector("[data-gespann-select]").value, 10);
			if (!gid) return null;
			var g = byId(pricing.gespanne, gid);
			if (!g || !g.tractor || !g.load) return null;
			var t = byId(pricing.tractors, g.tractor);
			var l = byId(pricing.loads, g.load);
			if (!t || !l) return null;
			return { ps: t.ps, loadCost: l.cost, machineIds: g.machines || [] };
		}
		var tid = parseInt(form.querySelector("[data-tractor-select]").value, 10);
		var lid = parseInt(form.querySelector("[data-load-select]").value, 10);
		if (!tid || !lid) return null;
		var tr = byId(pricing.tractors, tid);
		var lo = byId(pricing.loads, lid);
		if (!tr || !lo) return null;
		var ids = [];
		form.querySelectorAll("[data-machine]:checked").forEach(function (c) {
			ids.push(parseInt(c.value, 10));
		});
		return { ps: tr.ps, loadCost: lo.cost, machineIds: ids };
	}

	function update() {
		if (!isHours()) {
			var q = decVal(qtyEl), p = decVal(unitPriceEl);
			if (qtyCostEl) qtyCostEl.textContent = (q > 0 && p > 0) ? fmt(round2(q * p)) : "–";
			return;
		}
		var sel = resolveSelection();
		if (!sel) {
			rateEl.textContent = "–";
			costEl.textContent = "–";
			return;
		}
		var rate = round2(round2(sel.ps * sel.loadCost) + machineRates(sel.machineIds));
		rateEl.textContent = fmt(rate) + " / h";
		var hours = parseFloat((hoursEl.value || "0").replace(",", "."));
		costEl.textContent = hours > 0 ? fmt(round2(hours * rate)) : "–";
	}

	function applyMode() {
		var mode = currentMode();
		if (panels.gespann) panels.gespann.hidden = mode !== "gespann";
		if (panels.manual) panels.manual.hidden = mode !== "manual";
		update();
	}

	// Toggle the hour path (rig + rate) vs the quantity path (menge × unit price)
	// based on the chosen unit; hidden fields are barred from HTML validation.
	function applyUnit() {
		if (unitCustomWrap) unitCustomWrap.hidden = !(unitEl && unitEl.value === "__custom");
		var hours = isHours();
		if (hOnly) hOnly.hidden = !hours;
		if (qtyOnly) qtyOnly.hidden = hours;
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
		if (e.target.matches("[data-mode-toggle]")) applyMode();
		else if (e.target.matches("[data-unit], [data-unit-custom-input]")) applyUnit();
		else update();
	});

	var previews = form.querySelectorAll("[data-rate-preview]");
	function setLoading(on) { for (var i = 0; i < previews.length; i++) previews[i].classList.toggle("is-loading", on); }
	setLoading(true);
	fetch(form.getAttribute("data-pricing-url"), { credentials: "same-origin" })
		.then(function (r) { return r.json(); })
		.then(function (data) {
			pricing = {
				tractors: data.tractors || [],
				loads: data.loads || [],
				machines: data.machines || [],
				gespanne: data.gespanne || []
			};
			applyMode();
		})
		.catch(function () { /* preview stays inert; server still calculates */ })
		.then(function () { setLoading(false); });

	// Pre-save guard: warn (never block) on implausible hours or a same-day
	// duplicate of the same task. Keeps the form intact — a cancelled confirm just
	// stays on the page. requestSubmit() re-fires this handler with the flag set.
	form.addEventListener("submit", function (e) {
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
			if (typeof form.requestSubmit === "function") form.requestSubmit();
			else form.submit();
		}
	});

	applyMode();
	applyUnit();
})();
