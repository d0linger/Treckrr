// Live preview for structured counterclaim edits; saving remains a native POST.
(function () {
	"use strict";
	var form = document.querySelector("[data-ledger-booking]");
	if (!form) return;
	var field = function (name) { return form.elements.namedItem(name); };
	var number = function (name) { var el = field(name); return el && el.value.trim() ? Number(el.value.replace(",", ".")) : 0; };
	var round = function (n) { return Math.round((n + Number.EPSILON) * 100) / 100; };
	/** Shows a signed account effect, rounding each service independently. */
	function update() {
		var kind = field("booking_kind").value;
		var quantity = kind === "fixed" ? 1 : number(kind === "quantity" ? "quantity" : "hours");
		var price = number(kind === "fixed" ? "amount" : kind === "quantity" ? "unit_price" : kind === "labor" ? "partner_person_rate" : "partner_rate");
		var total = round(quantity * price), valid = quantity > 0 && price > 0;
		if (kind === "equipment") {
			var person = field("partner_person"), personRate = field("partner_person_rate"), personHours = field("partner_person_hours");
			var active = !!(person.value.trim() || personRate.value || personHours.value);
			person.required = active;
			personRate.required = active;
			if (active) {
				var hours = personHours.value ? number("partner_person_hours") : quantity;
				valid = valid && !!person.value.trim() && hours > 0 && number("partner_person_rate") > 0;
				total = round(total + round(hours * number("partner_person_rate")));
			}
		}
		form.querySelector("[data-ledger-preview]").textContent = valid && Number.isFinite(total) ?
			(field("booking_direction").value === "in" ? "Ich schulde dem Nachbarn: " : "Der Nachbar schuldet mir: ") + total.toLocaleString("de-DE", { style: "currency", currency: "EUR" }) : "Bitte vollständige, positive Stunden und Preise eingeben.";
	}
	form.addEventListener("input", update);
	form.addEventListener("change", update);
	update();
})();
