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

	// Client-side validation: German messages, an inline error element and ARIA
	// wiring so screen readers announce the problem (not just a transient native
	// bubble that vanishes on the next click).
	document.querySelectorAll("input, select, textarea").forEach(function (el) {
		function clear() {
			el.classList.remove("is-invalid");
			el.removeAttribute("aria-invalid");
			el.setCustomValidity("");
			var host = el.closest(".field") || el.parentNode;
			var box = host && host.querySelector(".field__err");
			if (box) { box.remove(); el.removeAttribute("aria-describedby"); }
		}
		el.addEventListener("invalid", function (e) {
			// Suppress the native validation bubble; the inline .field__err below
			// (wired via aria-describedby) is the visible message. The field stays
			// invalid, so the form still won't submit.
			e.preventDefault();
			var msg = "Bitte dieses Feld ausfüllen.";
			if (!el.validity.valueMissing) {
				msg = el.validity.tooShort ? "Eingabe ist zu kurz." : "Bitte einen gültigen Wert eingeben.";
			}
			el.setCustomValidity(msg);
			el.classList.add("is-invalid");
			el.setAttribute("aria-invalid", "true");
			var host = el.closest(".field") || el.parentNode;
			var box = host.querySelector(".field__err");
			if (!box) {
				box = document.createElement("span");
				box.className = "field__err";
				box.setAttribute("role", "alert");
				if (!el.id) el.id = "f" + Math.random().toString(36).slice(2, 8);
				box.id = el.id + "-err";
				el.setAttribute("aria-describedby", box.id);
				host.appendChild(box);
			}
			box.textContent = msg;
		});
		el.addEventListener("input", clear);
		el.addEventListener("change", clear);
	});

	// Submit feedback: a POST form that passes validation shows a spinning state
	// on its primary button. Submission still proceeds; the server redirects.
	// Skipped for data-confirm forms (the modal drives those via form.submit()).
	document.querySelectorAll("form").forEach(function (form) {
		if ((form.getAttribute("method") || "").toLowerCase() !== "post") return;
		form.addEventListener("submit", function (e) {
			if (form.hasAttribute("data-confirm") && form.dataset.confirmed !== "1") return;
			// Block a second submission (double-click or double-Enter) while the
			// first POST is in flight — the server redirects, so this navigates away.
			if (form.dataset.submitting === "1") { e.preventDefault(); return; }
			form.dataset.submitting = "1";
			var btn = form.querySelector("button.btn--primary[type='submit'], button[type='submit'].btn--primary");
			if (btn) { btn.classList.add("is-submitting"); btn.setAttribute("aria-busy", "true"); btn.disabled = true; }
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
			// Colour the confirm button by intent: irreversible deletes get red,
			// everything else keeps the primary colour.
			var okBtn = modal.querySelector("[data-modal-ok]");
			if (okBtn) {
				var danger = /löschen|entfernen|endgültig/i.test(message);
				okBtn.classList.toggle("btn--danger", danger);
				okBtn.classList.toggle("btn--primary", !danger);
			}
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

	// Recovery-code gate: "Fertig" stays disabled until the user confirms they
	// saved the codes. Without JS the link works normally (no lockout).
	(function () {
		var chk = document.querySelector("[data-gate-check]");
		var done = document.querySelector("[data-gate-done]");
		if (!chk || !done) return;
		function sync() {
			done.classList.toggle("is-disabled", !chk.checked);
			done.setAttribute("aria-disabled", chk.checked ? "false" : "true");
		}
		done.addEventListener("click", function (e) { if (!chk.checked) e.preventDefault(); });
		chk.addEventListener("change", sync);
		sync();
	})();

	// Password visibility toggles (the "eye").
	document.querySelectorAll("[data-pw-toggle]").forEach(function (btn) {
		btn.addEventListener("click", function () {
			var wrap = btn.closest(".pwwrap");
			var input = wrap && wrap.querySelector("input");
			if (!input) return;
			var show = input.type === "password";
			input.type = show ? "text" : "password";
			btn.setAttribute("aria-pressed", show ? "true" : "false");
			btn.setAttribute("aria-label", show ? "Passwort verbergen" : "Passwort anzeigen");
		});
	});

	// Live "passwords match" indicator on the change-password form. The server
	// re-checks the match; this is comfort feedback only.
	(function () {
		var np = document.querySelector("[data-pw-new]");
		var cp = document.querySelector("[data-pw-confirm]");
		var out = document.querySelector("[data-pw-match]");
		if (!np || !cp || !out) return;
		function check() {
			if (!cp.value) { out.textContent = ""; out.className = "pw-match"; cp.setCustomValidity(""); return; }
			var ok = np.value === cp.value;
			out.textContent = ok ? "Stimmt überein" : "Passwörter stimmen nicht überein";
			out.className = "pw-match " + (ok ? "pw-match--ok" : "pw-match--no");
			cp.setCustomValidity(ok ? "" : "Die Passwörter stimmen nicht überein.");
		}
		np.addEventListener("input", check);
		cp.addEventListener("input", check);
	})();

	// Generic copy-to-clipboard: [data-copy="#target"] copies the target's text.
	// Falls back to execCommand for non-secure (plain-HTTP) contexts where the
	// async clipboard API is unavailable — same pattern as recovery.js.
	document.querySelectorAll("[data-copy]").forEach(function (btn) {
		btn.addEventListener("click", function () {
			var target = document.querySelector(btn.getAttribute("data-copy"));
			if (!target) return;
			var text = target.textContent.trim();
			var done = function () {
				var prev = btn.innerHTML;
				btn.textContent = "Kopiert ✓";
				setTimeout(function () { btn.innerHTML = prev; }, 1500);
			};
			var fallback = function () {
				var ta = document.createElement("textarea");
				ta.value = text; document.body.appendChild(ta); ta.select();
				try { document.execCommand("copy"); } catch (e) { /* ignore */ }
				document.body.removeChild(ta); done();
			};
			if (navigator.clipboard && navigator.clipboard.writeText) {
				navigator.clipboard.writeText(text).then(done, fallback);
			} else { fallback(); }
		});
	});

	// Server-flash toast. Status toasts auto-hide after 4s; error toasts
	// (role="alert") persist until the user dismisses them (keyboard-operable
	// close button) or navigates away, so an error cannot vanish unnoticed.
	// (Copying recovery codes is handled by the page-scoped recovery.js.)
	var flash = document.querySelector(".toast");
	if (flash) {
		var dismissToast = function () {
			flash.style.transition = "opacity .3s";
			flash.style.opacity = "0";
			setTimeout(function () { flash.remove(); }, 300);
		};
		var closeBtn = flash.querySelector("[data-toast-dismiss]");
		if (closeBtn) closeBtn.addEventListener("click", dismissToast);
		if (flash.getAttribute("role") !== "alert") {
			setTimeout(dismissToast, 4000);
		}
	}

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
