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

	// Brand mark: pick one farm-machine symbol and keep it — stable across
	// refreshes and in-app navigation. It only changes when the stored mark is
	// missing (e.g. the browser cache / site data was cleared), where a fresh
	// random machine is chosen. The browser-tab favicon is kept in sync with it.
	(function () {
		var uses = document.querySelectorAll(".appbar__brand use, .auth__logo use");
		var MARKS = [
			"m-traktor", "m-anhaenger", "m-kipper", "m-pritsche", "m-rueckewagen",
			"m-ladewagen", "m-mulcher", "m-quad", "m-guellefass", "m-frontlader",
			"m-miststreuer", "m-ballenpresse", "m-feldspritze", "m-saemaschine",
			"m-teleskoplader", "m-kreiselschwader"
		];
		var LKEY = "treckrr-mark";
		var pick = null;
		try { pick = localStorage.getItem(LKEY); } catch (e) { /* storage unavailable */ }
		if (!pick || MARKS.indexOf(pick) < 0) {
			pick = MARKS[Math.floor(Math.random() * MARKS.length)];
			try { localStorage.setItem(LKEY, pick); } catch (e) {}
		}
		uses.forEach(function (u) { u.setAttribute("href", "#" + pick); });

		// Favicon: render the same machine as a green tile with white lines, so the
		// browser tab matches the in-app logo. Replaces the <link rel="icon"> node
		// (forces browsers to re-read it). data: URI is allowed by img-src.
		try {
			var sym = document.getElementById(pick);
			if (sym) {
				var fav = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">'
					+ '<rect width="24" height="24" rx="6" fill="#115638"/>'
					+ '<g fill="none" stroke="#ffffff" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">'
					+ sym.innerHTML + '</g></svg>';
				var href = "data:image/svg+xml," + encodeURIComponent(fav);
				var old = document.querySelector('link[rel="icon"]');
				if (old && old.parentNode) old.parentNode.removeChild(old);
				var link = document.createElement("link");
				link.rel = "icon"; link.type = "image/svg+xml"; link.href = href;
				document.head.appendChild(link);
			}
		} catch (e) { /* keep the static favicon */ }
	})();

	// Live text search over [data-search]'s target selector, plus an optional
	// "only open" companion checkbox ([data-open-filter]) that also hides rows
	// without a data-open marker. Both signals feed one visibility pass so they
	// never fight over the inline display value.
	document.querySelectorAll("[data-search]").forEach(function (input) {
		var sel = input.getAttribute("data-search");
		var openCb = document.querySelector("[data-open-filter]");
		function apply() {
			var q = input.value.toLowerCase();
			var onlyOpen = openCb && openCb.checked;
			document.querySelectorAll(sel).forEach(function (item) {
				var hit = item.textContent.toLowerCase().indexOf(q) >= 0;
				if (onlyOpen && !item.hasAttribute("data-open")) hit = false;
				item.style.display = hit ? "" : "none";
			});
		}
		input.addEventListener("input", apply);
		if (openCb) openCb.addEventListener("change", apply);
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

	// Side drawer (menu) open/close.
	(function () {
		var drawer = document.getElementById("drawer");
		if (!drawer) return;
		var scrim = document.querySelector(".drawer__scrim");
		var openers = document.querySelectorAll("[data-drawer-open]");
		var lastFocus = null;
		function setOpen(on) {
			if (on) lastFocus = document.activeElement;
			drawer.classList.toggle("is-open", on);
			drawer.setAttribute("aria-hidden", on ? "false" : "true");
			// inert keeps the closed (off-screen) drawer out of the tab order and
			// the accessibility tree — it is only hidden via CSS transform.
			if (on) { drawer.removeAttribute("inert"); } else { drawer.setAttribute("inert", ""); }
			if (scrim) scrim.hidden = !on;
			openers.forEach(function (b) { b.setAttribute("aria-expanded", on ? "true" : "false"); });
			// Move focus into the drawer on open, and restore it to the opener on
			// close, so keyboard/screen-reader users aren't stranded (a11y).
			if (on) {
				var first = drawer.querySelector("a, button, [tabindex]:not([tabindex='-1'])");
				if (first) first.focus();
			} else if (lastFocus && typeof lastFocus.focus === "function") {
				lastFocus.focus();
				// Clear it so a later stray Escape (drawer already closed) can't yank
				// focus back to the opener from wherever the user has since moved.
				lastFocus = null;
			}
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

	// ---- Beleg: print / copy-as-text / image export / Kostengrundlage ----
	(function () {
		var beleg = document.getElementById("beleg");
		if (!beleg) return;
		var scope = beleg.parentNode;

		function toast(msg) {
			var t = document.createElement("div");
			t.className = "beleg-toast";
			t.setAttribute("role", "status");
			t.textContent = msg;
			document.body.appendChild(t);
			requestAnimationFrame(function () { t.classList.add("is-on"); });
			setTimeout(function () {
				t.classList.remove("is-on");
				setTimeout(function () { t.remove(); }, 260);
			}, 1900);
		}
		function txt(el, sel) {
			var n = el.querySelector(sel);
			return n ? n.textContent.replace(/\s+/g, " ").trim() : "";
		}

		// Clean, label-based plain-text version of the currently shown beleg.
		function belegText() {
			var out = [];
			Array.prototype.forEach.call(beleg.children, function (el) {
				if (el.classList.contains("beleg__hero")) {
					out.push("Beleg · " + txt(el, ".beleg__who") + " · " + txt(el, ".beleg__yr"));
					out.push((txt(el, ".beleg__hl") || "Saldo") + ": " + txt(el, ".beleg__hv"));
					var hb = el.querySelector(".beleg__hb");
					if (hb) out.push(hb.textContent.replace(/\s+/g, " ").trim());
					out.push("--------------------------------");
				} else if (el.classList.contains("beleg__sec")) {
					out.push(el.textContent.trim() + ":");
				} else if (el.classList.contains("beleg__items")) {
					// Itemized Leistungen (grouped by day). Carry the day's date onto
					// its continuation rows, whose date cell is blank in the markup.
					if (beleg.classList.contains("beleg--bundle")) return;
					var lastDate = "";
					Array.prototype.forEach.call(el.children, function (c) {
						if (c.classList.contains("beleg__empty")) { out.push("  " + c.textContent.trim()); return; }
						if (!c.classList.contains("beleg__lrow")) return;
						var d = txt(c, ".beleg__d"), t = txt(c, ".beleg__t"),
							h = txt(c, ".beleg__h"), b = txt(c, ".beleg__b");
						if (d) { lastDate = d; } else { d = lastDate; }
						var line = "  " + d + " · " + t;
						if (h && h !== "—") line += " · " + h + " h";
						out.push(line + " · " + b);
					});
				} else if (el.classList.contains("beleg__bundle")) {
					// Aggregated Leistungen — only when the "Bündeln" view is active.
					if (!beleg.classList.contains("beleg--bundle")) return;
					Array.prototype.forEach.call(el.children, function (c) {
						if (!c.classList.contains("beleg__brow")) return;
						var t = txt(c, ".beleg__t"), h = txt(c, ".beleg__h"), b = txt(c, ".beleg__b");
						out.push("  " + t + (h ? " · " + h + " h" : "") + " · " + b);
					});
				} else if (el.classList.contains("beleg__lrow")) {
					// Verrechnung rows (direct children of the beleg).
					var d = txt(el, ".beleg__d"), t = txt(el, ".beleg__t"),
						h = txt(el, ".beleg__h"), b = txt(el, ".beleg__b");
					var line = "  " + d + " · " + t;
					if (h && h !== "—") line += " · " + h + " h";
					out.push(line + " · " + b);
				} else if (el.classList.contains("beleg__lsub")) {
					var c = el.children;
					var lbl = c[1] ? c[1].textContent.trim() : "";
					var hh = c[2] ? c[2].textContent.trim() : "";
					var bb = c[3] ? c[3].textContent.trim() : "";
					out.push("  " + lbl + ": " + (hh ? hh + " · " : "") + bb);
				} else if (el.classList.contains("beleg__grund")) {
					if (!beleg.classList.contains("beleg--grund")) return;
					out.push("");
					out.push(txt(el, ".beleg__grund-h"));
					Array.prototype.forEach.call(el.children, function (c) {
						var sp = c.querySelectorAll("span");
						if (c.classList.contains("beleg__gt--head") || c.classList.contains("beleg__gm--head")) {
							// The head row's bold entity word doubles as the section label.
							var cap = c.querySelector(".beleg__ghcap");
							if (cap) out.push(cap.textContent.trim() + ":");
						} else if (c.classList.contains("beleg__grund-h") || c.classList.contains("beleg__grund-sub")) {
							/* skip section header/sub */
						} else if (c.classList.contains("beleg__gt")) {
							var idEl = c.querySelector(".beleg__gt-id");
							if (idEl) {
								var psSmall = idEl.querySelector("small");
								var psTxt = psSmall ? psSmall.textContent.trim() : "";
								var idMain = idEl.textContent.replace(psTxt, "").replace(/\s+/g, " ").trim();
								if (idMain) out.push("  " + idMain + (psTxt ? " · " + psTxt : ""));
							}
							var blEl = c.querySelector(".beleg__gt-bl");
							var smEl = blEl ? blEl.querySelector("small") : null;
							var mach = smEl ? smEl.textContent.trim() : "";
							var bl = blEl ? blEl.textContent.replace(mach, "").replace(/\s+/g, " ").trim() : "";
							out.push("    " + bl + " · " + txt(c, ".beleg__gt-ps") + " €/PS·h · " + txt(c, ".beleg__gt-rt") + "/h" + (mach ? "  → " + mach : ""));
						} else if (c.classList.contains("beleg__gm")) {
							out.push("  " + sp[0].textContent.trim() + " · " + sp[1].textContent.trim() + " AB · " + sp[2].textContent.trim() + " €/AB·h · " + sp[3].textContent.trim() + "/h");
						}
					});
				} else if (el.classList.contains("beleg__foot")) {
					out.push("");
					out.push(el.textContent.trim());
				}
			});
			return out.join("\n");
		}
		function copyText(s) {
			if (navigator.clipboard && navigator.clipboard.writeText) {
				return navigator.clipboard.writeText(s);
			}
			return new Promise(function (res, rej) {
				var a = document.createElement("textarea");
				a.value = s; a.style.position = "fixed"; a.style.opacity = "0";
				document.body.appendChild(a); a.select();
				var ok = false;
				try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
				a.remove();
				if (ok) { res(); } else { rej(new Error("copy failed")); }
			});
		}

		// Image export: clone with inlined computed styles + inlined fonts,
		// rasterize via an SVG foreignObject → canvas → PNG. No external libs.
		function b64(buf) {
			var bytes = new Uint8Array(buf), s = "", chunk = 0x8000;
			for (var i = 0; i < bytes.length; i += chunk) {
				s += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
			}
			return btoa(s);
		}
		function inlineFonts() {
			var faces = [
				["Manrope", 800, "/static/fonts/manrope-800.woff2"],
				["Hanken Grotesk", 400, "/static/fonts/hanken-400.woff2"],
				["Hanken Grotesk", 600, "/static/fonts/hanken-600.woff2"],
				["JetBrains Mono", 500, "/static/fonts/jetbrainsmono-500.woff2"],
				["JetBrains Mono", 700, "/static/fonts/jetbrainsmono-700.woff2"]
			];
			return Promise.all(faces.map(function (f) {
				return fetch(f[2]).then(function (r) {
					if (!r.ok) throw new Error("font " + r.status);
					return r.arrayBuffer();
				}).then(function (buf) {
					return '@font-face{font-family:"' + f[0] + '";font-weight:' + f[1]
						+ ';src:url(data:font/woff2;base64,' + b64(buf) + ') format("woff2")}';
				}).catch(function () { return ""; });
			})).then(function (parts) { return parts.join(""); });
		}
		function inlineStyles(src, dst) {
			var cs = getComputedStyle(src), s = "";
			for (var i = 0; i < cs.length; i++) {
				var p = cs[i]; s += p + ":" + cs.getPropertyValue(p) + ";";
			}
			dst.setAttribute("style", s);
			var sc = src.children, dc = dst.children;
			for (var j = 0; j < sc.length; j++) inlineStyles(sc[j], dc[j]);
		}
		function belegPng() {
			var rect = beleg.getBoundingClientRect();
			var w = Math.ceil(rect.width), h = Math.ceil(beleg.scrollHeight);
			var clone = beleg.cloneNode(true);
			inlineStyles(beleg, clone);
			return inlineFonts().then(function (fontCss) {
				var xml = new XMLSerializer().serializeToString(clone);
				var svg = '<svg xmlns="http://www.w3.org/2000/svg" width="' + w + '" height="' + h + '">'
					+ '<foreignObject width="100%" height="100%">'
					+ '<div xmlns="http://www.w3.org/1999/xhtml" style="width:' + w + 'px">'
					+ '<style>' + fontCss + '</style>' + xml + '</div></foreignObject></svg>';
				var url = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
				return new Promise(function (res, rej) {
					var img = new Image();
					img.onload = function () {
						var scale = 2, cv = document.createElement("canvas");
						cv.width = w * scale; cv.height = h * scale;
						var ctx = cv.getContext("2d");
						ctx.scale(scale, scale);
						var bg = getComputedStyle(document.body).backgroundColor;
						if (!bg || bg === "transparent" || /rgba?\(\s*0\s*,\s*0\s*,\s*0\s*,\s*0\s*\)/.test(bg)) bg = "#fff";
						ctx.fillStyle = bg;
						ctx.fillRect(0, 0, w, h);
						ctx.drawImage(img, 0, 0);
						cv.toBlob(function (blob) { if (blob) { res(blob); } else { rej(new Error("toBlob null")); } }, "image/png");
					};
					img.onerror = function () { rej(new Error("svg load failed")); };
					img.src = url;
				});
			});
		}
		function download(blob, name) {
			var u = URL.createObjectURL(blob), a = document.createElement("a");
			a.href = u; a.download = name; document.body.appendChild(a); a.click();
			a.remove(); setTimeout(function () { URL.revokeObjectURL(u); }, 1000);
		}

		var printBtn = scope.querySelector("[data-beleg-print]");
		if (printBtn) printBtn.addEventListener("click", function () { window.print(); });

		var copyBtn = scope.querySelector("[data-beleg-copy]");
		if (copyBtn) copyBtn.addEventListener("click", function () {
			copyText(belegText()).then(
				function () { toast("Beleg als Text kopiert"); },
				function () { toast("Kopieren war nicht möglich"); });
		});

		var grundBtn = scope.querySelector("[data-beleg-grund]");
		if (grundBtn) grundBtn.addEventListener("click", function () {
			var on = !beleg.classList.contains("beleg--grund");
			beleg.classList.toggle("beleg--grund", on);
			grundBtn.setAttribute("aria-pressed", on ? "true" : "false");
		});

		var rechnungBtn = scope.querySelector("[data-beleg-rechnung]");
		if (rechnungBtn) rechnungBtn.addEventListener("click", function () {
			var on = !beleg.classList.contains("beleg--rechnung");
			beleg.classList.toggle("beleg--rechnung", on);
			rechnungBtn.setAttribute("aria-pressed", on ? "true" : "false");
		});

		var bundleBtn = scope.querySelector("[data-beleg-bundle]");
		if (bundleBtn) bundleBtn.addEventListener("click", function () {
			var on = !beleg.classList.contains("beleg--bundle");
			beleg.classList.toggle("beleg--bundle", on);
			bundleBtn.setAttribute("aria-pressed", on ? "true" : "false");
		});

		var imgBtn = scope.querySelector("[data-beleg-image]");
		if (imgBtn) imgBtn.addEventListener("click", function () {
			if (imgBtn.disabled) return;
			imgBtn.disabled = true;
			belegPng().then(function (blob) {
				var canClip = window.ClipboardItem && navigator.clipboard && navigator.clipboard.write;
				if (canClip) {
					return navigator.clipboard.write([new ClipboardItem({ "image/png": blob })]).then(
						function () { toast("Beleg als Bild kopiert"); },
						function () { download(blob, "beleg.png"); toast("Beleg als Bild gespeichert"); });
				}
				download(blob, "beleg.png"); toast("Beleg als Bild gespeichert");
			}).catch(function () {
				toast("Bild-Export hier nicht möglich – nutze Drucken/PDF");
			}).then(function () { imgBtn.disabled = false; });
		});

		// Share the Beleg as a PNG via the native share sheet (WhatsApp/Signal/…),
		// falling back to a download where file-sharing isn't supported.
		var shareBtn = scope.querySelector("[data-beleg-share]");
		if (shareBtn) shareBtn.addEventListener("click", function () {
			if (shareBtn.disabled) return;
			shareBtn.disabled = true;
			var name = (beleg.getAttribute("data-beleg-name") || "beleg") + ".png";
			belegPng().then(function (blob) {
				var file = new File([blob], name, { type: "image/png" });
				if (navigator.canShare && navigator.canShare({ files: [file] })) {
					return navigator.share({ files: [file], title: "Beleg" }).catch(function (e) {
						if (e && e.name === "AbortError") return; // user cancelled
						download(blob, name); toast("Beleg gespeichert");
					});
				}
				download(blob, name); toast("Teilen hier nicht möglich – Beleg gespeichert");
			}).catch(function () {
				toast("Bild-Export hier nicht möglich – nutze Drucken/PDF");
			}).then(function () { shareBtn.disabled = false; });
		});
	})();

	// Register the service worker for offline/PWA support.
	if ("serviceWorker" in navigator) {
		window.addEventListener("load", function () {
			navigator.serviceWorker.register("/sw.js").catch(function () { /* ignore */ });
		});
	}

	// ---- Backup panel: cron schedule builder (mode toggle + next-runs) -------
	(function () {
		var cols = document.querySelectorAll("[data-sched-col]");
		if (!cols.length) return;
		var DOW = ["Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"];
		function pad(n) { return String(n).padStart(2, "0"); }
		function num(s) { return /^\d+$/.test(s); }
		function describe(expr) {
			expr = (expr || "").trim();
			if (!expr) return { ok: true, t: "Ausgeschaltet (kein Zeitplan)." };
			var p = expr.split(/\s+/);
			if (p.length !== 5) return { ok: false, t: "Ungültig: erwartet 5 Felder (Minute Stunde Tag Monat Wochentag)." };
			var mi = p[0], ho = p[1], dom = p[2], mon = p[3], dow = p[4], m;
			var star = dom === "*" && mon === "*" && dow === "*";
			if ((m = mi.match(/^\*\/(\d+)$/)) && ho === "*" && star) return { ok: true, t: "Alle " + m[1] + " Minuten." };
			if (mi === "0" && ho === "*" && star) return { ok: true, t: "Stündlich (zur vollen Stunde)." };
			if ((m = ho.match(/^\*\/(\d+)$/)) && num(mi) && star) return { ok: true, t: "Alle " + m[1] + " Stunden (um Minute " + mi + ")." };
			if (num(mi) && num(ho) && mon === "*") {
				var time = pad(ho) + ":" + pad(mi) + " Uhr";
				if (dom === "*" && dow === "*") return { ok: true, t: "Täglich um " + time + "." };
				if (dom === "*" && num(dow)) return { ok: true, t: "Jeden " + DOW[parseInt(dow, 10) % 7] + " um " + time + "." };
				if (dom !== "*" && dow === "*") return { ok: true, t: "Monatlich am " + dom + ". um " + time + "." };
			}
			return { ok: true, t: "Cron: " + expr + " (Standardausdruck)." };
		}
		function match(f, v) {
			if (f === "*") return true;
			return f.split(",").some(function (part) {
				var mm = part.match(/^\*\/(\d+)$/);
				if (mm) return v % parseInt(mm[1], 10) === 0;
				return /^\d+$/.test(part) && parseInt(part, 10) === v;
			});
		}
		function nextRuns(expr, k) {
			expr = (expr || "").trim();
			var out = [];
			if (!expr) return out;
			var p = expr.split(/\s+/);
			if (p.length !== 5) return out;
			var t = new Date(Date.now() + 60000); t.setSeconds(0, 0);
			for (var i = 0; i < 60 * 24 * 60 && out.length < k; i++) {
				var d = t.getDay();
				if (match(p[0], t.getMinutes()) && match(p[1], t.getHours()) && match(p[2], t.getDate()) &&
					match(p[3], t.getMonth() + 1) && (p[4] === "*" || match(p[4], d) || (d === 0 && match(p[4], 7)))) out.push(new Date(t));
				t = new Date(t.getTime() + 60000);
			}
			return out;
		}
		var fmtChip = function (d) { return d.toLocaleString("de-DE", { weekday: "short", hour: "2-digit", minute: "2-digit" }); };
		var fmtNext = function (d) { return d.toLocaleString("de-DE", { day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit" }); };

		// Detect which friendly mode best represents a cron expression.
		function detectMode(expr) {
			var p = (expr || "").trim().split(/\s+/);
			if (p.length !== 5) return { mode: "cron" };
			var mi = p[0], ho = p[1], dom = p[2], mon = p[3], dow = p[4];
			if (num(mi) && num(ho) && mon === "*" && dom === "*" && dow === "*")
				return { mode: "daily", time: pad(ho) + ":" + pad(mi) };
			if (num(mi) && num(ho) && mon === "*" && dom === "*" && num(dow))
				return { mode: "weekly", time: pad(ho) + ":" + pad(mi), dow: dow };
			var m = ho.match(/^\*\/(\d+)$/);
			if (mi === "0" && m && dom === "*" && mon === "*" && dow === "*")
				return { mode: "hours", n: m[1] };
			return { mode: "cron" };
		}
		function buildRows(mode, init) {
			if (mode === "daily") return '<div class="bkp__arow">um <input type="time" data-k="time" value="' + (init.time || "03:00") + '"> Uhr</div>';
			if (mode === "hours") return '<div class="bkp__arow">alle <input class="input input--num" data-k="n" type="number" min="1" value="' + (init.n || 6) + '"> Stunden</div>';
			if (mode === "weekly") return '<div class="bkp__arow">jeden <select class="input" data-k="dow">' +
				[1, 2, 3, 4, 5, 6, 0].map(function (i) { return '<option value="' + i + '">' + DOW[i] + '</option>'; }).join("") +
				'</select> um <input type="time" data-k="time" value="' + (init.time || "03:00") + '"></div>';
			return ""; // cron mode: the raw input is shown instead
		}
		function cronFrom(col, mode) {
			var g = function (k) { return col.querySelector('[data-k="' + k + '"]'); };
			if (mode === "daily") { var t = (g("time").value || "03:00").split(":"); return (+t[1]) + " " + (+t[0]) + " * * *"; }
			if (mode === "hours") { return "0 */" + (g("n").value || 6) + " * * *"; }
			if (mode === "weekly") { var w = (g("time").value || "03:00").split(":"); return (+w[1]) + " " + (+w[0]) + " * * " + g("dow").value; }
			return col.querySelector(".bkp__cron-input").value;
		}

		Array.prototype.forEach.call(cols, function (col) {
			var seg = col.querySelector("[data-cron-modes]");
			var rows = col.querySelector(".bkp__cron-rows");
			var cronInput = col.querySelector(".bkp__cron-input");
			var cronLabel = col.querySelector(".bkp__cron-label");
			var desc = col.querySelector(".bkp__desc");
			var tlbl = col.querySelector(".bkp__tlbl");
			var timeline = col.querySelector(".bkp__timeline");
			var next = col.querySelector(".bkp__next");
			var init = { time: col.getAttribute("data-default-time") || "03:00", dow: col.getAttribute("data-default-dow") || "1" };
			var mode = "cron";

			function refresh() {
				var c = cronFrom(col, mode);
				cronInput.value = c; // canonical field that submits
				var r = describe(c);
				desc.textContent = r.t;
				desc.classList.toggle("bkp__desc--bad", !r.ok);
				var runs = r.ok ? nextRuns(c, 3) : [];
				tlbl.hidden = runs.length === 0;
				timeline.innerHTML = runs.map(function (d) { return '<span class="bkp__tl">' + fmtChip(d) + "</span>"; }).join("");
				next.innerHTML = "nächste Ausführung: <strong>" + (runs.length ? fmtNext(runs[0]) : "—") + "</strong>";
			}
			function render() {
				Array.prototype.forEach.call(seg.querySelectorAll("button"), function (b) { b.classList.toggle("on", b.getAttribute("data-m") === mode); });
				rows.innerHTML = buildRows(mode, init);
				var showCron = mode === "cron";
				cronInput.classList.toggle("bkp__hide", !showCron);
				if (cronLabel) cronLabel.classList.toggle("bkp__hide", !showCron);
				rows.querySelectorAll("input,select").forEach(function (el) { el.addEventListener("input", refresh); });
				if (mode === "weekly") { var s = rows.querySelector('[data-k="dow"]'); if (s) s.value = init.dow; }
				refresh();
			}

			var auto = col.querySelector("[data-auto]");
			seg.hidden = false;
			cronInput.addEventListener("input", function () { if (mode === "cron") refresh(); });
			Array.prototype.forEach.call(seg.querySelectorAll("button"), function (b) {
				b.addEventListener("click", function () { mode = b.getAttribute("data-m"); render(); });
			});

			// "Automatisch" toggles the whole destination on/off: unchecked writes an
			// empty cron (= off); checked seeds a sensible default the first time.
			function applyAuto(on, seed) {
				col.classList.toggle("bkp__off", !on);
				if (!on) {
					cronInput.value = "";
					desc.textContent = "Ausgeschaltet (kein Zeitplan).";
					desc.classList.remove("bkp__desc--bad");
					return;
				}
				if (seed && !cronInput.value.trim()) {
					var t = (init.time || "03:00").split(":");
					mode = "daily";
					cronInput.value = (+t[1]) + " " + (+t[0]) + " * * *";
				}
				var d = detectMode(cronInput.value);
				mode = d.mode;
				if (d.time) init.time = d.time;
				if (d.dow) init.dow = d.dow;
				if (d.n) init.n = d.n;
				render();
			}

			if (auto) {
				auto.checked = cronInput.value.trim() !== "";
				applyAuto(auto.checked, false);
				auto.addEventListener("change", function () { applyAuto(auto.checked, true); });
			} else {
				var det = detectMode(cronInput.value);
				mode = det.mode;
				if (det.time) init.time = det.time;
				if (det.dow) init.dow = det.dow;
				if (det.n) init.n = det.n;
				render();
			}
		});
	})();

	// ---- Backup restore: validate via fetch so the file/key survive ---------
	(function () {
		var form = document.querySelector("[data-restore-form]");
		if (!form) return;
		var btn = form.querySelector("[data-validate-btn]");
		var out = form.querySelector("[data-validate-result]");
		if (!btn || !out) return;
		btn.addEventListener("click", function (e) {
			e.preventDefault();
			if (!form.reportValidity()) return; // require file + key
			btn.disabled = true;
			out.hidden = false;
			out.classList.remove("bkp__desc--bad");
			out.textContent = "Prüfe …";
			fetch("/admin/backup/validate", {
				method: "POST",
				body: new FormData(form),
				headers: { "Accept": "application/json" },
				credentials: "same-origin",
			}).then(function (r) {
				if (r.status === 413) { throw new Error("Datei zu groß."); }
				if (!r.ok) { throw new Error("Validierung fehlgeschlagen (Status " + r.status + ")."); }
				return r.json();
			})
				.then(function (d) {
					out.textContent = d.message || (d.ok ? "Gültig." : "Fehler.");
					out.classList.toggle("bkp__desc--bad", !d.ok);
				})
				.catch(function (err) {
					out.textContent = (err && err.message) || "Validierung fehlgeschlagen.";
					out.classList.add("bkp__desc--bad");
				})
				.then(function () { btn.disabled = false; });
		});
	})();

	// Command palette (Ctrl/Cmd+K): fuzzy jump to a neighbor, invoice or gespann
	// via the /api/search endpoint. Debounced; keyboard-navigable.
	(function () {
		var ov = null, input = null, list = null, items = [], sel = -1, timer = null, seq = 0;
		var ICON = { neighbor: "👤", invoice: "📄", gespann: "🚜" };
		function close() { if (ov) { ov.remove(); ov = null; items = []; sel = -1; } }
		function open() {
			if (ov) return;
			ov = document.createElement("div"); ov.className = "cmdk"; ov.setAttribute("role", "dialog");
			ov.setAttribute("aria-label", "Suche");
			var box = document.createElement("div"); box.className = "cmdk__box";
			input = document.createElement("input"); input.className = "cmdk__input input";
			input.type = "search"; input.placeholder = "Suchen: Nachbar, Rechnung, Gespann …";
			input.setAttribute("aria-label", "Suche"); input.autocomplete = "off";
			list = document.createElement("ul"); list.className = "cmdk__list";
			box.appendChild(input); box.appendChild(list); ov.appendChild(box);
			ov.addEventListener("mousedown", function (e) { if (e.target === ov) close(); });
			input.addEventListener("input", function () { query(input.value); });
			input.addEventListener("keydown", onKey);
			document.body.appendChild(ov); input.focus();
		}
		function render(res) {
			items = res; sel = res.length ? 0 : -1; list.textContent = "";
			res.forEach(function (r, i) {
				var li = document.createElement("li");
				li.className = "cmdk__item" + (i === sel ? " is-sel" : "");
				li.setAttribute("role", "option");
				var ic = document.createElement("span"); ic.className = "cmdk__ic"; ic.textContent = ICON[r.kind] || "•";
				var tx = document.createElement("span"); tx.className = "cmdk__tx";
				var lb = document.createElement("span"); lb.className = "cmdk__lb"; lb.textContent = r.label;
				tx.appendChild(lb);
				if (r.sub) { var sb = document.createElement("span"); sb.className = "cmdk__sub"; sb.textContent = r.sub; tx.appendChild(sb); }
				li.appendChild(ic); li.appendChild(tx);
				li.addEventListener("mousedown", function (e) { e.preventDefault(); go(i); });
				list.appendChild(li);
			});
		}
		function highlight() {
			Array.prototype.forEach.call(list.children, function (li, i) { li.classList.toggle("is-sel", i === sel); });
		}
		function go(i) { var r = items[i]; if (r && r.url) window.location.href = r.url; }
		function query(q) {
			clearTimeout(timer);
			if (q.trim().length < 2) { render([]); return; }
			var mine = ++seq;
			timer = setTimeout(function () {
				fetch("/api/search?q=" + encodeURIComponent(q.trim()), { credentials: "same-origin" })
					.then(function (r) { return r.ok ? r.json() : []; })
					.then(function (res) { if (mine === seq) render(res || []); })
					.catch(function () { if (mine === seq) render([]); });
			}, 180);
		}
		function onKey(e) {
			if (e.key === "Escape") { e.preventDefault(); close(); }
			else if (e.key === "ArrowDown") { e.preventDefault(); if (items.length) { sel = (sel + 1) % items.length; highlight(); } }
			else if (e.key === "ArrowUp") { e.preventDefault(); if (items.length) { sel = (sel - 1 + items.length) % items.length; highlight(); } }
			else if (e.key === "Enter") { e.preventDefault(); if (sel >= 0) go(sel); }
		}
		document.addEventListener("keydown", function (e) {
			if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) { e.preventDefault(); if (ov) close(); else open(); }
		});
	})();

	// Keyboard shortcuts: "/" focuses search, "g" then d/n/s/m/y/p navigates, "?"
	// toggles a cheatsheet. Ignored while typing in a field so normal input works.
	(function () {
		var nav = { d: "/", n: "/neighbors", s: "/stats", m: "/mahnwesen", y: "/years", p: "/prices" };
		var gPending = false, gTimer = null;
		function typing(el) {
			if (!el) return false;
			var t = (el.tagName || "").toLowerCase();
			return t === "input" || t === "textarea" || t === "select" || el.isContentEditable;
		}
		function help() {
			var ex = document.getElementById("kbd-help");
			if (ex) { ex.remove(); return; }
			var rows = [
				["Strg K", "Schnellsuche (Palette)"], ["/", "Suche fokussieren"],
				["g d", "Übersicht"], ["g n", "Nachbarn"], ["g s", "Statistik"],
				["g m", "Mahnwesen"], ["g y", "Jahre"], ["g p", "Preise"],
				["?", "Diese Hilfe"], ["Esc", "Schließen"]
			];
			var ov = document.createElement("div");
			ov.id = "kbd-help"; ov.className = "kbd-help"; ov.setAttribute("role", "dialog");
			ov.setAttribute("aria-label", "Tastaturkürzel");
			var card = document.createElement("div"); card.className = "kbd-help__card";
			var h = document.createElement("h2"); h.className = "kbd-help__h"; h.textContent = "Tastaturkürzel";
			card.appendChild(h);
			rows.forEach(function (r) {
				var row = document.createElement("div"); row.className = "kbd-help__row";
				var k = document.createElement("kbd"); k.textContent = r[0];
				var d = document.createElement("span"); d.textContent = r[1];
				row.appendChild(k); row.appendChild(d); card.appendChild(row);
			});
			ov.appendChild(card);
			ov.addEventListener("click", function (e) { if (e.target === ov) ov.remove(); });
			document.body.appendChild(ov);
		}
		document.addEventListener("keydown", function (e) {
			if (e.key === "Escape") { var h = document.getElementById("kbd-help"); if (h) h.remove(); }
			if (e.ctrlKey || e.metaKey || e.altKey || typing(e.target)) return;
			if (gPending) {
				gPending = false; clearTimeout(gTimer);
				var url = nav[e.key];
				if (url) { e.preventDefault(); window.location.href = url; }
				return;
			}
			if (e.key === "g") { gPending = true; gTimer = setTimeout(function () { gPending = false; }, 1200); return; }
			if (e.key === "/") {
				var box = document.querySelector('input[type="search"]:not([hidden])');
				if (box) { e.preventDefault(); box.focus(); }
			} else if (e.key === "?") { e.preventDefault(); help(); }
		});
	})();
})();
