	/** Wires the side drawer's visibility, inert state and keyboard focus restoration when it exists. */
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
		/** Handles Escape and focus wrapping only while the drawer is open. */
		document.addEventListener("keydown", function (e) {
			if (!drawer.classList.contains("is-open")) return;
			if (e.key === "Escape") { e.preventDefault(); setOpen(false); }
			else trapDialogFocus(drawer, e);
		});
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

