/** Device-local operator favorites for the field workflow. */
(function () {
	"use strict";
	var user = document.querySelector('meta[name="user-id"]');
	if (!user || !window.localStorage) return;
	var prefix = "treckrr:favorites:v1:" + user.content + ":";

	function read(kind) {
		try {
			var value = JSON.parse(localStorage.getItem(prefix + kind) || "[]");
			return Array.isArray(value) ? value.filter(function (item) { return typeof item === "string"; }).slice(0, 8) : [];
		} catch (_) {
			return [];
		}
	}

	function write(kind, values) {
		try { localStorage.setItem(prefix + kind, JSON.stringify(values.slice(0, 8))); } catch (_) { /* Storage is optional. */ }
	}

	function toggle(kind, value) {
		if (!value) return false;
		var values = read(kind);
		var index = values.indexOf(value);
		if (index >= 0) values.splice(index, 1);
		else values.unshift(value);
		write(kind, values);
		return index < 0;
	}

	function updateButton(button, active) {
		button.classList.toggle("is-favorite", active);
		button.setAttribute("aria-pressed", active ? "true" : "false");
		var label = active ? "Favorit entfernen" : "Als Favorit merken";
		button.setAttribute("title", label);
		button.setAttribute("aria-label", label);
		var text = button.querySelector("[data-favorite-label]");
		if (text) text.textContent = active ? "Favorit" : "Merken";
	}

	function sortItems(list, kind) {
		var favorites = read(kind);
		var rows = Array.prototype.slice.call(list.querySelectorAll(":scope > [data-favorite-id]"));
		rows.sort(function (a, b) {
			var ai = favorites.indexOf(a.getAttribute("data-favorite-id"));
			var bi = favorites.indexOf(b.getAttribute("data-favorite-id"));
			if (ai < 0) ai = Number.MAX_SAFE_INTEGER;
			if (bi < 0) bi = Number.MAX_SAFE_INTEGER;
			return ai - bi;
		});
		rows.forEach(function (row) { list.appendChild(row); });
	}

	document.querySelectorAll("[data-favorite-list]").forEach(function (list) {
		var kind = list.getAttribute("data-favorite-list");
		sortItems(list, kind);
		list.querySelectorAll("[data-favorite-id]").forEach(function (row) {
			var id = row.getAttribute("data-favorite-id");
			var button = row.querySelector("[data-favorite-toggle]");
			if (!button) return;
			updateButton(button, read(kind).indexOf(id) >= 0);
			button.addEventListener("click", function () {
				updateButton(button, toggle(kind, id));
				sortItems(list, kind);
			});
		});
	});

	document.querySelectorAll("[data-favorite-select]").forEach(function (select) {
		var kind = select.getAttribute("data-favorite-select");
		var button = document.querySelector('[data-favorite-select-toggle="' + kind + '"]');
		function active() { return read(kind).indexOf(select.value) >= 0; }
		function sortOptions() {
			var selected = select.value;
			var favorites = read(kind);
			var options = Array.prototype.slice.call(select.options).filter(function (option) { return option.value; });
			options.sort(function (a, b) {
				var ai = favorites.indexOf(a.value), bi = favorites.indexOf(b.value);
				if (ai < 0) ai = Number.MAX_SAFE_INTEGER;
				if (bi < 0) bi = Number.MAX_SAFE_INTEGER;
				return ai - bi;
			});
			options.forEach(function (option) { select.appendChild(option); });
			select.value = selected;
		}
		sortOptions();
		if (!button) return;
		updateButton(button, active());
		button.disabled = !select.value;
		select.addEventListener("change", function () {
			button.disabled = !select.value;
			updateButton(button, active());
		});
		button.addEventListener("click", function () {
			if (!select.value) return;
			updateButton(button, toggle(kind, select.value));
			sortOptions();
		});
	});

	document.querySelectorAll("[data-favorite-text]").forEach(function (box) {
		var kind = box.getAttribute("data-favorite-text");
		var input = box.querySelector("[data-favorite-text-input]");
		var button = box.querySelector("[data-favorite-text-toggle]");
		var list = box.querySelector("[data-favorite-text-list]");
		if (!input || !button || !list) return;
		function value() { return input.value.trim(); }
		function render() {
			var current = value();
			var values = read(kind);
			list.replaceChildren();
			values.forEach(function (favorite) {
				var choice = document.createElement("button");
				choice.type = "button";
				choice.className = "favorite-chip";
				choice.textContent = favorite;
				choice.addEventListener("click", function () {
					input.value = favorite;
					input.dispatchEvent(new Event("input", { bubbles: true }));
					input.focus();
					render();
				});
				list.appendChild(choice);
			});
			button.disabled = !current;
			updateButton(button, values.indexOf(current) >= 0);
		}
		input.addEventListener("input", render);
		button.addEventListener("click", function () { if (value()) toggle(kind, value()); render(); });
		render();
	});
})();
