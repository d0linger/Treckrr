/** Unsaved local drafts are separate from the durable offline submission queue. */
(function () {
	"use strict";
	var form = document.querySelector("[data-unified-booking][data-entry-create]");
	if (!form || !form.querySelector("[data-booking-draft-offer]")) return;
	var field = function (name) { return form.elements.namedItem(name); };
	// Retain the clicked action through the asynchronous precheck's resubmit,
	// including the native path where the original submitter is no longer sent.
	form.addEventListener("click", function (event) {
		var button = event.target.closest("button");
		if (button && button.type === "submit") field("continue_booking").value = button.hasAttribute("data-booking-next") ? "1" : "0";
	}, true);
	// The native fallback has no confirmed-save event. Do not offer or persist
	// drafts there: a successful POST could otherwise be restored and booked again.
	if (!("indexedDB" in window) || !window.fetch || !window.URLSearchParams || !window.FormData) return;
	var user = document.querySelector('meta[name="user-id"]');
	if (!user) return;
	var key = "treckrr:booking-draft:v1:" + user.content + ":" + field("year_id").value + ":" + field("neighbor_id").value;
	var offer = form.querySelector("[data-booking-draft-offer]"), status = form.querySelector("[data-booking-draft-status]");
	var saved = null, dirty = false, restoring = false, queued = false, conflicted = false, lastStored = null;
	var paused = [];
	function resumeEditing() { paused.forEach(function (el) { el.inert = false; }); paused = []; }
	var excluded = ["csrf_token", "year_id", "neighbor_id", "booking_form_version", "continue_booking", "copy_mode", "signature_name", "signature_png", "signature_at"];
	function controls() {
		return Array.from(form.querySelectorAll("input[name], select[name], textarea[name]")).filter(function (el) {
			return excluded.indexOf(el.name) < 0 && el.type !== "submit" && el.type !== "file" && !el.closest("[data-person-row][hidden]");
		});
	}
	try {
		lastStored = localStorage.getItem(key);
		var candidate = JSON.parse(lastStored || "null");
		if (candidate && candidate.version === 1 && (!candidate.formVersion || candidate.formVersion === "2" || candidate.formVersion === "3") && Array.isArray(candidate.fields) && candidate.fields.length <= 300 && candidate.fields.every(function (f) { return typeof f.name === "string" && typeof f.value === "string" && f.value.length <= 2000 && excluded.indexOf(f.name) < 0; })) saved = candidate;
	} catch (_) { /* Corrupt/private storage must not stop booking. */ }
	if (saved) {
		offer.hidden = false;
		// Resolve the old draft explicitly before accepting new input. Otherwise
		// new edits could neither replace the old draft nor survive navigation.
		paused = Array.from(form.children).filter(function (el) { return el !== offer && el !== status && el.tagName !== "INPUT" && !el.inert; });
		paused.forEach(function (el) { el.inert = true; });
		status.textContent = "Bitte zuerst den vorhandenen Entwurf fortsetzen oder verwerfen. Danach ist die Erfassung wieder freigegeben.";
		if (form.hasAttribute("data-booking-locked")) { status.textContent = "Der Entwurf bleibt erhalten. Dieses Konto ist derzeit für neue Buchungen gesperrt."; form.querySelector("[data-booking-draft-restore]").disabled = true; }
	}
	function save() {
		if (!dirty || restoring || saved || queued || form.hasAttribute("data-booking-locked")) return;
		var snapshot = { version: 1, formVersion: field("booking_form_version").value, fields: controls().map(function (el) { return { name: el.name, value: el.value, checked: el.checked === true }; }), people: form.querySelectorAll("[data-person-row]:not([hidden])").length };
		try {
			if (localStorage.getItem(key) !== lastStored) {
				conflicted = true;
				status.textContent = "Ein anderer Tab hat den Entwurf geändert. Deine Eingaben hier werden nicht darübergeschrieben. Bitte diesen Tab geöffnet lassen und die Buchung speichern oder offline vormerken, bevor du ihn verlässt.";
				return;
			}
			var encoded = JSON.stringify(snapshot);
			localStorage.setItem(key, encoded);
			lastStored = encoded;
			conflicted = false;
			status.textContent = "Ungespeicherter Entwurf auf diesem Gerät. Noch keine Buchung und nicht in der Offline-Warteschlange.";
		}
		catch (_) { conflicted = true; status.textContent = "Entwurf kann auf diesem Gerät nicht gesichert werden. Bitte die Seite bis zum Speichern geöffnet lassen."; }
	}
	function clear() {
		try {
			// A confirmation for this form must never erase another tab's draft.
			if (localStorage.getItem(key) === lastStored) { localStorage.removeItem(key); lastStored = null; }
		} catch (_) { /* Optional storage. */ }
		saved = null; dirty = false; conflicted = false; offer.hidden = true; resumeEditing();
	}
	function changed() {
		if (!restoring) {
			dirty = true; queued = false;
			// Capture after the shared editor has filled person rates/names and
			// updated visibility in its later input/change listeners.
			queueMicrotask(save);
		}
	}
	form.addEventListener("input", changed);
	form.addEventListener("change", changed);
	form.addEventListener("click", function (event) {
		var button = event.target.closest("button");
		if (button && (button.hasAttribute("data-person-add") || button.hasAttribute("data-person-remove"))) changed();
	}, true);
	form.addEventListener("submit", function (event) { if (saved) { event.preventDefault(); event.stopImmediatePropagation(); offer.querySelector("button").focus(); return; } save(); }, true);
	form.querySelector("[data-booking-draft-discard]").addEventListener("click", function () { clear(); status.textContent = "Gesicherter Entwurf verworfen. Aktuelle Eingaben bleiben unverändert."; });
	form.querySelector("[data-booking-draft-restore]").addEventListener("click", function () {
		if (!saved || form.hasAttribute("data-booking-locked") || !form.treckrrBooking) return;
		restoring = true;
		resumeEditing();
		var snapshot = saved;
		// Pre-upgrade drafts were V2 account postings. Restoring must not silently
		// turn an own flat rate into a newly invoice-bearing charge.
		field("booking_form_version").value = snapshot.formVersion || "2";
		function setControl(el, item) {
			if (el.tagName === "SELECT" && !Array.from(el.options).some(function (o) { return o.value === item.value; })) return;
			if (el.type === "checkbox" || el.type === "radio") el.checked = item.checked === true;
			else el.value = item.value;
		}
		["booking_kind", "booking_direction", "mode"].forEach(function (name) {
			var values = snapshot.fields.filter(function (f) { return f.name === name; });
			Array.from(form.querySelectorAll('[name="' + name + '"]')).forEach(function (el, i) { if (values[i]) setControl(el, values[i]); });
			var first = form.querySelector('[name="' + name + '"]'); if (first) first.dispatchEvent(new Event("change", { bubbles: true }));
		});
		var count = Math.min(20, Math.max(1, Number(snapshot.people) || 1));
		while (form.querySelectorAll("[data-person-row]:not([hidden])").length < count) {
			var before = form.querySelectorAll("[data-person-row]:not([hidden])").length;
			form.querySelector("[data-person-add]").click();
			if (form.querySelectorAll("[data-person-row]:not([hidden])").length === before) break;
		}
		var indices = Object.create(null);
		controls().forEach(function (el) {
			var i = indices[el.name] || 0; indices[el.name] = i + 1;
			var items = snapshot.fields.filter(function (f) { return f.name === el.name; });
			var choice = el.type === "radio" || el.type === "checkbox";
			var item = choice ? items.find(function (f) { return f.value === el.value; }) : items[i];
			if (item) setControl(el, item); else if (choice) el.checked = false;
		});
		form.treckrrBooking.refreshVisibility();
		// Current-page pricing (never the stored preview) is recalculated by the
		// existing input listener; the server still validates prices and locks.
		form.querySelector('[name="hours"]').dispatchEvent(new Event("input", { bubbles: true }));
		saved = null; offer.hidden = true; restoring = false; dirty = true; save();
		status.textContent = "Entwurf wiederhergestellt. Bitte Datum, aktuelle Preise und Personen prüfen; gespeichert wird erst beim Absenden.";
	});
	form.addEventListener("treckrr:booking-saved", function () { clear(); });
	form.addEventListener("treckrr:booking-queued", function () { clear(); queued = true; status.textContent = "Buchung in der Offline-Warteschlange – nicht nochmals eingeben. Die Warteschlange zeigt den Sendestatus."; });
	window.addEventListener("pagehide", save);
	window.addEventListener("storage", function (event) {
		if (event.key === key && dirty) save();
	});
	window.addEventListener("beforeunload", function (event) {
		if (dirty && conflicted) { event.preventDefault(); event.returnValue = ""; }
	});
})();
