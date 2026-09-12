// Progressive form enhancements preserve native controls and server submissions.
(function () {
	"use strict";

	// Reveal every enclosing disclosure before focusing the first invalid control.
	var focusScheduled = false;
	document.addEventListener("invalid", function (event) {
		var field = event.target;
		for (var parent = field.parentElement; parent; parent = parent.parentElement) {
			if (parent.tagName === "DETAILS") parent.open = true;
		}
		if (!focusScheduled) {
			focusScheduled = true;
			requestAnimationFrame(function () {
				field.focus();
				focusScheduled = false;
			});
		}
	}, true);

	document.querySelectorAll("[data-machine-picker]").forEach(function (picker) {
		var rows = Array.from(picker.querySelectorAll("label.check"));
		if (!rows.length) return;
		var toolbar = document.createElement("div");
		toolbar.className = "machine-picker__tools";
		var label = document.createElement("label");
		label.className = "field";
		var title = document.createElement("span");
		title.textContent = "Maschinen suchen";
		var search = document.createElement("input");
		search.type = "search";
		search.className = "input";
		search.placeholder = "Name oder Kategorie …";
		label.append(title, search);
		var selected = document.createElement("button");
		selected.type = "button";
		selected.className = "btn btn--ghost btn--sm";
		selected.setAttribute("aria-pressed", "false");
		var status = document.createElement("p");
		status.className = "muted small";
		status.setAttribute("role", "status");
		toolbar.append(label, selected);
		picker.before(toolbar);
		picker.after(status);
		picker.classList.add("machine-picker__options");

		/** Updates matching rows and selection feedback without disabling or clearing IDs. */
		function refresh() {
			var query = search.value.trim().toLocaleLowerCase("de");
			var selectedOnly = selected.getAttribute("aria-pressed") === "true";
			var checked = 0;
			var visible = 0;
			rows.forEach(function (row) {
				var active = row.querySelector("input").checked;
				if (active) checked++;
				var text = (row.textContent + " " + (row.dataset.category || "")).toLocaleLowerCase("de");
				row.hidden = !text.includes(query) || (selectedOnly && !active);
				if (!row.hidden) visible++;
			});
			selected.textContent = "Nur ausgewählte (" + checked + ")";
			status.textContent = visible ? visible + " von " + rows.length + " angezeigt · " + checked + " ausgewählt" : "Keine Treffer. Suche oder Auswahlfilter zurücksetzen.";
		}
		search.addEventListener("input", refresh);
		// Enter in a search narrows choices; it must not save the enclosing form.
		search.addEventListener("keydown", function (event) {
			if (event.key === "Enter") event.preventDefault();
		});
		selected.addEventListener("click", function () {
			selected.setAttribute("aria-pressed", selected.getAttribute("aria-pressed") === "true" ? "false" : "true");
			refresh();
		});
		picker.addEventListener("change", refresh);
		if (picker.closest("form")) picker.closest("form").addEventListener("reset", function () {
			setTimeout(function () {
				search.value = "";
				selected.setAttribute("aria-pressed", "false");
				refresh();
			}, 0);
		});
		refresh();
	});
})();
