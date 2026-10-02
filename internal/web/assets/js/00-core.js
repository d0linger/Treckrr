/** Adds optional client-side interactions to Treckrr's server-rendered pages without external dependencies. */
(function () {
	"use strict";

	/** Returns visible, enabled tab stops for custom dialogs that lack native <dialog> focus handling. */
	function dialogFocusables(root) {
		return Array.prototype.filter.call(root.querySelectorAll(
			'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
		), function (el) { return !el.hidden && el.getClientRects().length > 0; });
	}
	/** Wraps Tab/Shift+Tab at a custom dialog's boundaries; blocks Tab when no focusable control exists. */
	function trapDialogFocus(root, e) {
		if (e.key !== "Tab") return;
		var nodes = dialogFocusables(root);
		if (!nodes.length) { e.preventDefault(); return; }
		var first = nodes[0], last = nodes[nodes.length - 1];
		if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
		else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
	}
	window.TreckrrDialog = { trapFocus: trapDialogFocus };

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

	// Backup-health dot: tap toggles the popover (hover/focus already reveal it via
	// CSS on desktop). Closes on an outside click or Escape — needed on touch, where
	// there is no hover.
	(function () {
		var wrap = document.querySelector("[data-bk]");
		if (!wrap) return;
		var btn = wrap.querySelector("[data-bk-toggle]");
		if (!btn) return;
		function close() { wrap.classList.remove("is-open"); btn.setAttribute("aria-expanded", "false"); }
		btn.addEventListener("click", function (e) {
			e.stopPropagation();
			var open = wrap.classList.toggle("is-open");
			btn.setAttribute("aria-expanded", open ? "true" : "false");
		});
		document.addEventListener("click", function (e) { if (!wrap.contains(e.target)) close(); });
		document.addEventListener("keydown", function (e) { if (e.key === "Escape") close(); });
	})();

	// Popup menus built on native <details data-popmenu> (e.g. the Beleg "Link"
	// panel): close on outside click or Escape — details alone only toggles on
	// its summary. Presentation only.
	(function () {
		var menus = document.querySelectorAll("details[data-popmenu]");
		if (!menus.length) return;
		function closeAll() { menus.forEach(function (d) { d.removeAttribute("open"); }); }
		document.addEventListener("click", function (e) {
			menus.forEach(function (d) { if (!d.contains(e.target)) d.removeAttribute("open"); });
		});
		document.addEventListener("keydown", function (e) { if (e.key === "Escape") closeAll(); });
	})();

	// Brand mark: keep a farm-machine symbol stable during normal use — F5 and
	// in-app navigation reuse the stored pick — and re-roll it to a different one
	// only on a hard reload (Ctrl+Shift+R), which bypasses the service worker so
	// the page loads uncontrolled. The tab favicon is kept in sync below.
	(function () {
		var uses = document.querySelectorAll(".appbar__brand use, .auth__logo use");
		var MARKS = [
			"m-traktor", "m-anhaenger", "m-kipper", "m-pritsche", "m-rueckewagen",
			"m-ladewagen", "m-mulcher", "m-quad", "m-guellefass", "m-frontlader",
			"m-miststreuer", "m-ballenpresse", "m-feldspritze", "m-saemaschine",
			"m-teleskoplader", "m-kreiselschwader"
		];
		var LKEY = "treckrr-mark";
		// A hard reload bypasses the service worker → the page loads with no
		// controller, whereas F5 and navigation stay controlled. A missing
		// controller alone is NOT proof of that: on a first visit, and whenever
		// registration is blocked (private window, blocked site data, policy),
		// there is never a controller and every plain F5 would look "hard". So only
		// trust the signal once the worker has actually controlled this browser at
		// least once, and otherwise degrade to once-per-session — the same rule
		// used when the browser has no service worker at all.
		var hard = (function () {
			var SEEN = "treckrr-sw-ctrl", SESS = LKEY + "-s";
			if ("serviceWorker" in navigator) {
				var controlled = false;
				try { controlled = !!navigator.serviceWorker.controller; } catch (x) {}
				if (controlled) { try { localStorage.setItem(SEEN, "1"); } catch (e) {} return false; }
				try { if (localStorage.getItem(SEEN) === "1") return true; } catch (e) {}
			}
			try {
				var fresh = !sessionStorage.getItem(SESS);
				if (fresh) sessionStorage.setItem(SESS, "1");
				return fresh;
			} catch (x) { return true; }
		})();
		var last = null;
		try { last = localStorage.getItem(LKEY); } catch (e) { /* storage unavailable */ }
		var pick;
		if (last && MARKS.indexOf(last) >= 0 && !hard) {
			pick = last; // controlled load (F5 / navigation): keep the stored mark
		} else {
			var pool = MARKS.filter(function (m) { return m !== last; });
			pick = pool[Math.floor(Math.random() * pool.length)];
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

