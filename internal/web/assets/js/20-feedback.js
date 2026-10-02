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
		// Capture the original label once, so a second click within the flash
		// window restores it rather than pinning a transient "Kopiert ✓".
		var orig = btn.innerHTML;
		var timer = null;
		var flash = function (label) {
			btn.textContent = label;
			if (timer) clearTimeout(timer);
			timer = setTimeout(function () { btn.innerHTML = orig; timer = null; }, 1500);
		};
		btn.addEventListener("click", function () {
			var target = document.querySelector(btn.getAttribute("data-copy"));
			if (!target) return;
			var text = target.textContent.trim();
			var done = function () { flash("Kopiert ✓"); };
			// Fallback for non-secure (plain-HTTP) contexts. Only report success
			// when execCommand actually copied; a false return or a thrown error
			// shows a failure message instead of a misleading "Kopiert ✓".
			var fallback = function () {
				var ta = document.createElement("textarea");
				ta.value = text; document.body.appendChild(ta); ta.select();
				var ok = false;
				try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
				document.body.removeChild(ta);
				ok ? done() : flash("Fehlgeschlagen");
			};
			if (navigator.clipboard && navigator.clipboard.writeText) {
				try {
					navigator.clipboard.writeText(text).then(done, fallback);
				} catch (e) { fallback(); }
			} else { fallback(); }
		});
	});

	// Login 2FA: toggle the single second-factor field between authenticator
	// code (numeric, 6 digits) and backup/recovery code (alphanumeric). Same
	// field name ("totp") — the server accepts either, so no round trip needed.
	(function () {
		var toggle = document.querySelector("[data-2fa-toggle]");
		var input = document.querySelector("[data-2fa-input]");
		if (!toggle || !input) return;
		var label = document.querySelector("[data-2fa-label]");
		var hint = document.querySelector("[data-2fa-hint]");
		var recovery = false;
		var apply = function (refocus) {
			input.classList.remove("otp", "otp2");
			if (recovery) {
				input.classList.add("otp2");
				input.setAttribute("inputmode", "text");
				input.removeAttribute("pattern");
				input.removeAttribute("maxlength");
				input.placeholder = input.getAttribute("data-recovery-placeholder");
				if (label) label.textContent = "Backup‑Code";
				if (hint) hint.textContent = hint.getAttribute("data-recovery-hint");
				toggle.textContent = toggle.getAttribute("data-to-app");
			} else {
				input.classList.add("otp");
				input.setAttribute("inputmode", "numeric");
				input.setAttribute("pattern", "[0-9]*");
				input.setAttribute("maxlength", "6");
				input.placeholder = input.getAttribute("data-app-placeholder");
				if (label) label.textContent = "Zwei‑Faktor‑Code";
				if (hint) hint.textContent = hint.getAttribute("data-app-hint");
				toggle.textContent = toggle.getAttribute("data-to-recovery");
			}
			if (refocus) { input.value = ""; input.focus(); }
		};
		apply(false); // enhance the default (app) mode with numeric constraints
		toggle.addEventListener("click", function () { recovery = !recovery; apply(true); });
	})();

	// Sparkline value tooltip. Native <title> covers desktop hover; a tap (or
	// hover) on a point's hit column also shows a small positioned bubble so the
	// value is reachable on touch. element.style is CSSOM (allowed by the CSP).
	(function () {
		var hits = document.querySelectorAll(".spark__hit[data-tip]");
		if (!hits.length) return;
		var tip = null;
		var show = function (el) {
			var t = el.getAttribute("data-tip");
			if (!t) return;
			if (!tip) {
				tip = document.createElement("span");
				tip.className = "sparktip";
				document.body.appendChild(tip);
			}
			tip.textContent = t;
			var r = el.getBoundingClientRect();
			tip.style.left = (r.left + r.width / 2) + "px";
			tip.style.top = (r.top - 4) + "px";
			tip.classList.add("is-on");
		};
		var hide = function () { if (tip) tip.classList.remove("is-on"); };
		hits.forEach(function (el) {
			el.addEventListener("click", function (e) { e.stopPropagation(); show(el); });
			el.addEventListener("mouseenter", function () { show(el); });
			el.addEventListener("mouseleave", hide);
			el.addEventListener("focus", function () { show(el); });
			el.addEventListener("blur", hide);
		});
		document.addEventListener("click", hide);
	})();

	// Plain status toasts auto-hide after 4s. Errors and actionable feedback
	// (especially Undo) stay available until dismissal or navigation.
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
		if (flash.getAttribute("role") !== "alert" && !flash.querySelector("form, a, button:not([data-toast-dismiss])")) {
			setTimeout(dismissToast, 4000);
		}
	}

	// Copy a companion input's value to the clipboard (e.g. the public Beleg link).
	document.querySelectorAll("[data-copy-src-btn]").forEach(function (btn) {
		btn.addEventListener("click", function () {
			var input = btn.parentElement.querySelector("[data-copy-src]");
			if (!input) return;
			input.select();
			var done = function () { var t = btn.textContent; btn.textContent = "Kopiert ✓"; setTimeout(function () { btn.textContent = t; }, 1500); };
			var legacy = function () { try { if (document.execCommand("copy")) done(); } catch (e) {} };
			if (navigator.clipboard) { navigator.clipboard.writeText(input.value).then(done).catch(legacy); }
			else { legacy(); }
		});
	});

	// Print trigger (CSP-safe replacement for an inline onclick handler).
	document.querySelectorAll("[data-print]").forEach(function (btn) {
		btn.addEventListener("click", function () { window.print(); });
	});

