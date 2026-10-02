	/** Wires Ctrl/Cmd+K search with local navigation, debounced /api/search results and keyboard selection. */
	(function () {
		var ov = null, input = null, list = null, items = [], sel = -1, timer = null, seq = 0, lastFocus = null;
		// Werkblatt F4: stamped mono type plates per result kind instead of emoji —
		// the palette speaks the same machine-ledger voice as the rest of the app.
		var ICON = { neighbor: "NB", invoice: "RE", base: "GL", tractor: "TR", machine: "MA", load: "ST", gespann: "GE", nav: "NAV" };

		// Static navigation targets: category/section names ("Grundlagen", "Jahre") are
		// not data rows, so they can't come from /api/search — offer them as jump
		// destinations, matched on their label + keywords (accent-folded).
		var COMMANDS = [
			{ label: "Übersicht", sub: "Dashboard", url: "/", kw: "start home dashboard uebersicht" },
			{ label: "Nachbarn", sub: "verwalten", url: "/neighbors", kw: "nachbar kontakte" },
			{ label: "Grundlagen", sub: "Traktoren · Maschinen · Belastungsstufen · Preise", url: "/bases", kw: "grundlagen bemessung preise preisliste traktoren maschinen belastungsstufen laststufen gespanne" },
			{ label: "Jahre", sub: "Abrechnungsjahre", url: "/years", kw: "jahr abrechnung" },
			{ label: "Mahnwesen", sub: "Offene Posten", url: "/mahnwesen", kw: "mahnung offen faellig zahlung" }
		];
		function fold(x) { return x.toLowerCase().normalize("NFKD").replace(/[̀-ͯ]/g, ""); }
		function localCommands(q) {
			var f = fold(q);
			return COMMANDS.filter(function (c) { return fold(c.label + " " + c.kw).indexOf(f) !== -1; })
				.map(function (c) { return { kind: "nav", label: c.label, sub: c.sub, url: c.url }; });
		}
		/**
		 * Closes the palette, restores focus and invalidates in-flight search results so
		 * reopening cannot select an unseen result from the previous search.
		 */
		function close() {
			if (!ov) return;
			ov.remove(); ov = null; items = []; sel = -1; seq++;
			if (lastFocus && typeof lastFocus.focus === "function") lastFocus.focus();
			lastFocus = null;
		}
		/** Creates and focuses the search dialog once, preserving the opener for close-time focus restoration. */
		function open() {
			if (ov) return;
			lastFocus = document.activeElement;
			ov = document.createElement("div"); ov.className = "cmdk"; ov.setAttribute("role", "dialog");
			ov.setAttribute("aria-modal", "true");
			ov.setAttribute("aria-label", "Suche");
			var box = document.createElement("div"); box.className = "cmdk__box";
			input = document.createElement("input"); input.className = "cmdk__input input";
			input.type = "search"; input.placeholder = "Suchen: Nachbar, Rechnung, Traktor, Grundlagen …";
			input.setAttribute("aria-label", "Suche"); input.setAttribute("role", "combobox");
			input.setAttribute("aria-autocomplete", "list"); input.setAttribute("aria-controls", "cmdk-list");
			input.setAttribute("aria-expanded", "true"); input.autocomplete = "off";
			list = document.createElement("ul"); list.className = "cmdk__list"; list.id = "cmdk-list";
			list.setAttribute("role", "listbox");
			box.appendChild(input); box.appendChild(list); ov.appendChild(box);
			ov.addEventListener("mousedown", function (e) { if (e.target === ov) close(); });
			ov.addEventListener("keydown", function (e) { trapDialogFocus(ov, e); });
			input.addEventListener("input", function () { query(input.value); });
			input.addEventListener("keydown", onKey);
			document.body.appendChild(ov); input.focus();
		}
		/** Replaces palette results using text nodes and synchronizes the initial selection's ARIA state. */
		function render(res) {
			items = res; sel = res.length ? 0 : -1; list.textContent = "";
			/** Builds one selectable result without interpreting its label or subtitle as HTML. */
			res.forEach(function (r, i) {
				var li = document.createElement("li");
				li.className = "cmdk__item" + (i === sel ? " is-sel" : "");
				li.id = "cmdk-opt-" + i; li.setAttribute("role", "option");
				li.setAttribute("aria-selected", i === sel ? "true" : "false");
				var ic = document.createElement("span");
				ic.className = "cmdk__ic" + (r.kind === "nav" ? " cmdk__ic--nav" : "");
				ic.textContent = ICON[r.kind] || "··";
				var tx = document.createElement("span"); tx.className = "cmdk__tx";
				var lb = document.createElement("span"); lb.className = "cmdk__lb"; lb.textContent = r.label;
				tx.appendChild(lb);
				if (r.sub) { var sb = document.createElement("span"); sb.className = "cmdk__sub"; sb.textContent = r.sub; tx.appendChild(sb); }
				li.appendChild(ic); li.appendChild(tx);
				li.addEventListener("mousedown", function (e) { e.preventDefault(); go(i); });
				list.appendChild(li);
			});
			if (sel >= 0) input.setAttribute("aria-activedescendant", "cmdk-opt-" + sel);
			else input.removeAttribute("aria-activedescendant");
		}
		/** Keeps visual selection, screen-reader active descendant and scrolling aligned with the selected index. */
		function highlight() {
			Array.prototype.forEach.call(list.children, function (li, i) {
				li.classList.toggle("is-sel", i === sel);
				li.setAttribute("aria-selected", i === sel ? "true" : "false");
			});
			if (sel >= 0) {
				input.setAttribute("aria-activedescendant", "cmdk-opt-" + sel);
				var active = list.children[sel];
				if (active && active.scrollIntoView) active.scrollIntoView({ block: "nearest" });
			}
		}
		function go(i) { var r = items[i]; if (r && r.url) window.location.href = r.url; }
		function query(q) {
			clearTimeout(timer);
			var t = q.trim();
			// seq++ so a fetch already in flight from a longer query can't repaint stale
			// hits once the input narrows below the 2-char threshold.
			if (t.length < 2) { seq++; render([]); return; }
			// Show matching nav targets instantly; append the debounced API data hits.
			var local = localCommands(t);
			render(local);
			var mine = ++seq;
			timer = setTimeout(function () {
				fetch("/api/search?q=" + encodeURIComponent(t), { credentials: "same-origin" })
					.then(function (r) { return r.ok ? r.json() : []; })
					.then(function (res) { if (mine === seq) render(local.concat(res || [])); })
					.catch(function () { if (mine === seq) render(local); });
			}, 180);
		}
		function onKey(e) {
			if (e.key === "Escape") { e.preventDefault(); close(); }
			else if (e.key === "ArrowDown") { e.preventDefault(); if (items.length) { sel = (sel + 1) % items.length; highlight(); } }
			else if (e.key === "ArrowUp") { e.preventDefault(); if (items.length) { sel = (sel - 1 + items.length) % items.length; highlight(); } }
			else if (e.key === "Enter") { e.preventDefault(); if (sel >= 0) go(sel); }
		}
		document.querySelectorAll("[data-cmdk-open]").forEach(function (btn) {
			btn.addEventListener("click", function () { if (ov) close(); else open(); });
		});
		document.addEventListener("keydown", function (e) {
			if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) { e.preventDefault(); if (ov) close(); else open(); }
		});
	})();

	/** Adds navigation/search shortcuts and a help dialog while leaving ordinary text entry unaffected. */
	(function () {
		var nav = { d: "/", n: "/neighbors", s: "/stats", m: "/mahnwesen", y: "/years", p: "/prices" };
		var gPending = false, gTimer = null, helpLastFocus = null;
		function typing(el) {
			if (!el) return false;
			var t = (el.tagName || "").toLowerCase();
			return t === "input" || t === "textarea" || t === "select" || el.isContentEditable;
		}
		/** Removes the shortcut help dialog and returns focus to its opener when available. */
		function closeHelp() {
			var ex = document.getElementById("kbd-help");
			if (!ex) return;
			ex.remove();
			if (helpLastFocus && typeof helpLastFocus.focus === "function") helpLastFocus.focus();
			helpLastFocus = null;
		}
		/** Toggles the shortcut reference dialog with an initially focused close control and keyboard focus wrapping. */
		function help() {
			var ex = document.getElementById("kbd-help");
			if (ex) { closeHelp(); return; }
			helpLastFocus = document.activeElement;
			var rows = [
				["Strg K", "Schnellsuche (Palette)"], ["/", "Suche fokussieren"],
				["g d", "Übersicht"], ["g n", "Nachbarn"], ["g s", "Statistik"],
				["g m", "Mahnwesen"], ["g y", "Jahre"], ["g p", "Preise"],
				["?", "Diese Hilfe"], ["Esc", "Schließen"]
			];
			var ov = document.createElement("div");
			ov.id = "kbd-help"; ov.className = "kbd-help"; ov.setAttribute("role", "dialog");
			ov.setAttribute("aria-modal", "true");
			ov.setAttribute("aria-label", "Tastaturkürzel");
			var card = document.createElement("div"); card.className = "kbd-help__card";
			var head = document.createElement("div"); head.className = "kbd-help__head";
			var h = document.createElement("h2"); h.className = "kbd-help__h"; h.textContent = "Tastaturkürzel";
			var close = document.createElement("button"); close.type = "button"; close.className = "btn btn--ghost btn--sm";
			close.textContent = "Schließen"; close.addEventListener("click", closeHelp);
			head.appendChild(h); head.appendChild(close); card.appendChild(head);
			rows.forEach(function (r) {
				var row = document.createElement("div"); row.className = "kbd-help__row";
				var k = document.createElement("kbd"); k.textContent = r[0];
				var d = document.createElement("span"); d.textContent = r[1];
				row.appendChild(k); row.appendChild(d); card.appendChild(row);
			});
			ov.appendChild(card);
			ov.addEventListener("click", function (e) { if (e.target === ov) closeHelp(); });
			/** Closes help on Escape and keeps Tab navigation inside the dialog. */
			ov.addEventListener("keydown", function (e) {
				if (e.key === "Escape") { e.preventDefault(); closeHelp(); }
				else trapDialogFocus(ov, e);
			});
			document.body.appendChild(ov);
			close.focus();
		}
		/** Dispatches unmodified shortcuts outside editable fields, with a 1.2-second window for g-prefix navigation. */
		document.addEventListener("keydown", function (e) {
			if (e.key === "Escape" && document.getElementById("kbd-help")) { closeHelp(); return; }
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
