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
		.catch(function () { /* preview stays inert; server still calculates */ });

	applyMode();
	applyUnit();
})();
