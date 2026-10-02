	// Register the service worker for offline/PWA support.
	if ("serviceWorker" in navigator) {
		window.addEventListener("load", function () {
			navigator.serviceWorker.register("/sw.js").catch(function () { /* ignore */ });
		});
	}

	/** Enhances backup schedule fields with friendly modes and a client-side preview of upcoming runs. */
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
		/** Builds labeled schedule controls from preset/numeric values; raw cron mode needs no extra rows. */
		function buildRows(mode, init) {
			if (mode === "daily") return '<div class="bkp__arow">um <input type="time" data-k="time" aria-label="Uhrzeit" value="' + (init.time || "03:00") + '"> Uhr</div>';
			if (mode === "hours") return '<div class="bkp__arow">alle <input class="input input--num" data-k="n" aria-label="Intervall in Stunden" type="number" min="1" value="' + (init.n || 6) + '"> Stunden</div>';
			if (mode === "weekly") return '<div class="bkp__arow">jeden <select class="input" data-k="dow" aria-label="Wochentag">' +
				[1, 2, 3, 4, 5, 6, 0].map(function (i) { return '<option value="' + i + '">' + DOW[i] + '</option>'; }).join("") +
				'</select> um <input type="time" data-k="time" aria-label="Uhrzeit" value="' + (init.time || "03:00") + '"></div>';
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

