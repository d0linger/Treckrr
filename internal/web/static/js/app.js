// Treckrr — progressive enhancement. No external dependencies.
(function () {
	"use strict";

	// Theme persistence: mirror the server-chosen theme into localStorage and
	// re-apply it on pages that render without the cookie (login, offline, or a
	// service-worker-cached shell). The cookie remains the primary mechanism.
	(function () {
		var KEY = "treckrr-theme";
		var html = document.documentElement;
		try {
			var stored = localStorage.getItem(KEY);
			var current = html.getAttribute("data-theme") || "auto";
			if (stored) {
				if (current !== stored) html.setAttribute("data-theme", stored);
			} else if (current !== "auto") {
				localStorage.setItem(KEY, current);
			}
		} catch (e) { /* storage unavailable */ }
		document.querySelectorAll("[data-theme-set]").forEach(function (a) {
			a.addEventListener("click", function () {
				try { localStorage.setItem(KEY, a.getAttribute("data-theme-set")); } catch (e) {}
			});
		});
	})();

	// Live text search: filter items matching [data-search]'s target selector.
	document.querySelectorAll("[data-search]").forEach(function (input) {
		var sel = input.getAttribute("data-search");
		input.addEventListener("input", function () {
			var q = input.value.toLowerCase();
			document.querySelectorAll(sel).forEach(function (item) {
				var hit = item.textContent.toLowerCase().indexOf(q) >= 0;
				item.style.display = hit ? "" : "none";
			});
		});
	});

	// Auto-submit the enclosing form when a marked select changes.
	document.querySelectorAll("select[data-autosubmit]").forEach(function (sel) {
		sel.addEventListener("change", function () {
			if (sel.form) sel.form.submit();
		});
	});

	// Category filter for master-data lists (prices page).
	document.querySelectorAll("[data-filter]").forEach(function (input) {
		var targetSel = input.getAttribute("data-filter");
		input.addEventListener("change", function () {
			var val = input.value;
			document.querySelectorAll(targetSel).forEach(function (row) {
				var cat = row.getAttribute("data-category") || "";
				row.style.display = (!val || cat === val) ? "" : "none";
			});
		});
	});

	// Carry-over: toggle all neighbour checkboxes at once.
	document.querySelectorAll("[data-carry-toggle-all]").forEach(function (btn) {
		btn.addEventListener("click", function () {
			var form = btn.closest("form");
			if (!form) return;
			var boxes = form.querySelectorAll("[data-carry-check]");
			var anyChecked = Array.prototype.some.call(boxes, function (b) { return b.checked; });
			boxes.forEach(function (b) { b.checked = !anyChecked; });
		});
	});

	// Client-side validation: German messages + highlight invalid fields.
	document.querySelectorAll("input, select, textarea").forEach(function (el) {
		el.addEventListener("invalid", function () {
			el.classList.add("is-invalid");
			if (el.validity.valueMissing) {
				el.setCustomValidity("Bitte dieses Feld ausfüllen.");
			} else if (el.validity.rangeUnderflow || el.validity.badInput) {
				el.setCustomValidity("Bitte einen gültigen Wert eingeben.");
			} else if (el.validity.tooShort) {
				el.setCustomValidity("Eingabe ist zu kurz.");
			}
		});
		el.addEventListener("input", function () {
			el.classList.remove("is-invalid");
			el.setCustomValidity("");
		});
	});

	// Confirm destructive actions with a modern modal dialog (falls back to
	// native confirm when <dialog> is unsupported).
	var modal = document.getElementById("confirmModal");
	var msgEl = modal ? modal.querySelector("[data-modal-msg]") : null;
	var inputEl = modal ? modal.querySelector("[data-modal-input]") : null;
	var pendingForm = null;

	if (modal && typeof modal.showModal === "function") {
		modal.addEventListener("close", function () {
			var form = pendingForm;
			pendingForm = null;
			if (modal.returnValue === "confirm" && form) {
				// Copy an optional reason (e.g. void reason) into the form before submit.
				if (inputEl && !inputEl.hidden) {
					var target = form.querySelector("input[name='reason']");
					if (target) target.value = inputEl.value.trim();
				}
				form.dataset.confirmed = "1";
				form.submit(); // does not re-trigger the submit listener
			}
		});
	}

	document.querySelectorAll("form[data-confirm]").forEach(function (form) {
		form.addEventListener("submit", function (e) {
			if (form.dataset.confirmed === "1") return;
			var message = form.getAttribute("data-confirm");
			var reasonLabel = form.getAttribute("data-confirm-reason");
			if (!modal || typeof modal.showModal !== "function") {
				if (!window.confirm(message)) { e.preventDefault(); return; }
				// Native fallback: prompt for the reason if one was requested.
				if (reasonLabel !== null) {
					var target = form.querySelector("input[name='reason']");
					if (target) target.value = (window.prompt(reasonLabel) || "").trim();
				}
				return;
			}
			e.preventDefault();
			pendingForm = form;
			if (msgEl) msgEl.textContent = message;
			if (inputEl) {
				if (reasonLabel !== null) {
					inputEl.hidden = false;
					inputEl.placeholder = reasonLabel;
					inputEl.value = "";
				} else {
					inputEl.hidden = true;
				}
			}
			modal.returnValue = "";
			modal.showModal();
			if (inputEl && !inputEl.hidden) inputEl.focus();
		});
	});

	// Auto-hide the server-flash toast after a short delay. Scoped to .toast so
	// static inline .flash info boxes are never faded out by mistake.
	var flash = document.querySelector(".toast");
	if (flash) {
		setTimeout(function () {
			flash.style.transition = "opacity .4s";
			flash.style.opacity = "0";
			setTimeout(function () { flash.remove(); }, 400);
		}, 4000);
	}

	// Copy recovery codes to the clipboard (one per line).
	document.querySelectorAll("[data-copy-codes]").forEach(function (btn) {
		btn.addEventListener("click", function () {
			var list = document.querySelector("[data-codes]");
			if (!list) return;
			var codes = Array.prototype.map.call(list.querySelectorAll("code"), function (c) {
				return c.textContent.trim();
			}).join("\n");
			if (navigator.clipboard && navigator.clipboard.writeText) {
				navigator.clipboard.writeText(codes).then(function () {
					var prev = btn.textContent;
					btn.textContent = "Kopiert ✓";
					setTimeout(function () { btn.textContent = prev; }, 1500);
				}).catch(function () { /* clipboard denied */ });
			}
		});
	});

	// Print trigger (CSP-safe replacement for an inline onclick handler).
	document.querySelectorAll("[data-print]").forEach(function (btn) {
		btn.addEventListener("click", function () { window.print(); });
	});

	// Side drawer (menu) open/close.
	(function () {
		var drawer = document.getElementById("drawer");
		if (!drawer) return;
		var scrim = document.querySelector(".drawer__scrim");
		var openers = document.querySelectorAll("[data-drawer-open]");
		function setOpen(on) {
			drawer.classList.toggle("is-open", on);
			drawer.setAttribute("aria-hidden", on ? "false" : "true");
			// inert keeps the closed (off-screen) drawer out of the tab order and
			// the accessibility tree — it is only hidden via CSS transform.
			if (on) { drawer.removeAttribute("inert"); } else { drawer.setAttribute("inert", ""); }
			if (scrim) scrim.hidden = !on;
			openers.forEach(function (b) { b.setAttribute("aria-expanded", on ? "true" : "false"); });
		}
		openers.forEach(function (b) { b.addEventListener("click", function () { setOpen(true); }); });
		document.querySelectorAll("[data-drawer-close]").forEach(function (b) {
			b.addEventListener("click", function () { setOpen(false); });
		});
		document.addEventListener("keydown", function (e) { if (e.key === "Escape") setOpen(false); });
	})();

	// Instant dark/light toggle: apply immediately, mirror to localStorage, and
	// persist the cookie in the background so server-rendered pages match.
	(function () {
		var toggles = document.querySelectorAll("[data-theme-toggle]");
		if (!toggles.length) return;
		toggles.forEach(function (btn) {
			btn.addEventListener("click", function (e) {
				e.preventDefault();
				var next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
				document.documentElement.setAttribute("data-theme", next);
				try { localStorage.setItem("treckrr-theme", next); } catch (err) {}
				fetch("/theme?set=" + next, { credentials: "same-origin" }).catch(function () {});
			});
		});
	})();

	// Register the service worker for offline/PWA support.
	if ("serviceWorker" in navigator) {
		window.addEventListener("load", function () {
			navigator.serviceWorker.register("/sw.js").catch(function () { /* ignore */ });
		});
	}
})();
