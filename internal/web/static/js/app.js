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
					out.push("Saldo: " + txt(el, ".beleg__hv"));
					var hb = el.querySelector(".beleg__hb");
					if (hb) out.push(hb.textContent.replace(/\s+/g, " ").trim());
					out.push("--------------------------------");
				} else if (el.classList.contains("beleg__sec")) {
					out.push(el.textContent.trim() + ":");
				} else if (el.classList.contains("beleg__lrow")) {
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
					out.push("  " + lbl + ": " + (hh ? hh + " h · " : "") + bb);
				} else if (el.classList.contains("beleg__grund")) {
					if (!beleg.classList.contains("beleg--grund")) return;
					out.push("");
					out.push(txt(el, ".beleg__grund-h"));
					Array.prototype.forEach.call(el.children, function (c) {
						var sp = c.querySelectorAll("span");
						if (c.classList.contains("beleg__gcap")) {
							out.push(c.textContent.trim() + ":");
						} else if (c.classList.contains("beleg__gt--head") || c.classList.contains("beleg__gm--head") ||
							c.classList.contains("beleg__grund-h") || c.classList.contains("beleg__grund-sub")) {
							/* skip captions/headers */
						} else if (c.classList.contains("beleg__gt-t")) {
							out.push("  " + c.textContent.trim());
						} else if (c.classList.contains("beleg__gt")) {
							out.push("    " + sp[0].textContent.trim() + " · " + sp[1].textContent.trim() + " €/PS·h · " + sp[2].textContent.trim() + "/h");
						} else if (c.classList.contains("beleg__gt-m")) {
							out.push("      → " + c.textContent.trim());
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
				["JetBrains Mono", 500, "/static/fonts/jetbrainsmono-500.woff2"]
			];
			return Promise.all(faces.map(function (f) {
				return fetch(f[2]).then(function (r) { return r.arrayBuffer(); }).then(function (buf) {
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
						ctx.fillStyle = getComputedStyle(document.body).backgroundColor || "#fff";
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
	})();

	// Register the service worker for offline/PWA support.
	if ("serviceWorker" in navigator) {
		window.addEventListener("load", function () {
			navigator.serviceWorker.register("/sw.js").catch(function () { /* ignore */ });
		});
	}
})();
