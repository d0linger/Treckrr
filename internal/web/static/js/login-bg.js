/* Login worksheet background.
 *
 * Paints a paper "worksheet" onto the pre-auth <canvas id="login-bg">, dusted
 * with Treckrr's full 16-machine icon set — the same glyphs the appbar picks
 * from — each drawn at most once (no repeats), scattered across the whole sheet
 * like a field of favicons. A slow light band travels across and lifts each
 * column of machines as it passes.
 *
 * One of two sheets is chosen at random on every load, the way the appbar picks
 * a random machine mark: "graph" (fine engineering grid, green ink, a small
 * orange dimension line under each machine) or "hatch" (engraved diagonal
 * hatch, plain ink emboss).
 *
 * Colours are read from the live CSS design tokens (--bg, --text, --primary,
 * --signal, --muted), so the sheet tracks the active theme (Hell / Nachtschicht)
 * and re-themes on the fly. Honours prefers-reduced-motion by painting a single
 * static frame and never animating. Purely decorative: aria-hidden and
 * pointer-inert, so it never touches the form. CSP-safe — no inline code, all
 * drawing happens on the canvas.
 */
(function () {
	"use strict";
	var canvas = document.getElementById("login-bg");
	if (!canvas) return;
	var ctx = canvas.getContext("2d", { alpha: false });
	if (!ctx) return; // ancient browser: leave the canvas transparent, page still fine

	/* ---- colour helpers -------------------------------------------------- */
	/* Tokens are authored as #rrggbb; tolerate rgb()/#rgb just in case. */
	function parse(v) {
		v = (v || "").trim();
		if (v.charAt(0) === "#") {
			var h = v.slice(1);
			if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2];
			return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
		}
		var m = v.match(/(\d+(?:\.\d+)?)/g);
		if (m && m.length >= 3) return [+m[0], +m[1], +m[2]];
		return [0, 0, 0];
	}
	function RGB(a) { return "rgb(" + (a[0] | 0) + "," + (a[1] | 0) + "," + (a[2] | 0) + ")"; }
	function mix(a, b, t) { return RGB([a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t]); }
	function palette() {
		var cs = getComputedStyle(document.documentElement);
		function tok(n, f) { return parse(cs.getPropertyValue(n) || f); }
		return {
			paper: tok("--bg", "#edf1ec"), ink: tok("--text", "#19211d"),
			green: tok("--primary", "#115638"), dim: tok("--muted", "#57645c"),
			orange: tok("--signal", "#e05910")
		};
	}

	/* ---- the 16 machine glyphs (layout.html sprite) as canvas paths ------ */
	/* c: circles [cx,cy,r] · q: rounded tank [x,y,w,h,r] · p: stroked path data. */
	var ICONS = [
		{ c: [[7.5, 16.5, 3.8], [18, 18, 2.4]], p: ["M11.3 16.5h4.3", "M4.3 12.7V6.5h5.9l2 6.2", "M12.2 12.7h4.6V15.6"] },
		{ c: [[5.6, 17.4, 2.5], [15, 18, 1.9], [19.6, 18, 1.9]], p: ["M3.2 15V9.4h4l1.1 3.6", "M8.3 15h4.4v-3.2h8.5V15", "M12.7 12h-1.3"] },
		{ c: [[8.5, 18.4, 2], [15.5, 18.4, 2]], p: ["M4 15.8h13", "M4 15.8 1.6 16.4", "M6.6 6.8 17 10.6 15.4 14 5 10.2z", "M9.6 15.6 8.9 11.4", "M16.2 13.8l2.1 1"] },
		{ c: [[8, 17.8, 2], [16, 17.8, 2]], p: ["M3 13.4h17v1.4H3z", "M3 13.4V9.4", "M6 15.8h4M14 15.8h4"] },
		{ c: [[7, 18, 1.9], [11.5, 18, 1.9], [6, 13.2, 1.3], [8.8, 13.2, 1.3], [7.4, 10.9, 1.3]], p: ["M3.5 15.5h11", "M16 15.5V6.5", "M16 6.5h3.3", "M19.3 6.5 17.6 10.4", "M17.6 10.4l-1 1.6M17.6 10.4l1.2 1.2"] },
		{ c: [[8.4, 18, 2], [15.8, 18, 2]], p: ["M4.2 15.5 6 7.4h11v8.1z", "M4.2 15.5h12.8", "M2 16.5c1.1-1.1 2.7-1.1 3.8 0"] },
		{ p: ["M4.5 10.3h15v4.2h-15z", "M4.7 14.5 6.6 16.6 8.5 14.5 10.4 16.6 12.3 14.5 14.2 16.6 16.1 14.5 18 16.6 19.4 15", "M8.5 10.3 12 6.6 15.5 10.3", "M12 6.6V5.1", "M4.5 12.4H2.6"] },
		{ c: [[6.5, 16.5, 2.6], [17.5, 16.5, 2.6]], p: ["M4 14c.5-1.7 2.1-2.7 4.2-2.7h3.1l1.4-2h2.1l1.1 2c2.1.2 3.4 1.3 3.9 2.7", "M8.8 11.2 6.7 7.9H4.4", "M4.4 7.9h3.2", "M15.5 11.8h4"] },
		{ c: [[9, 18, 2], [16, 18, 2]], q: [[4, 9.4, 16, 5.8, 2.9]], p: ["M4 12.3H1.5", "M17.4 9.6v5.4"] },
		{ c: [[9, 17.2, 2.8], [17.6, 17.8, 1.9]], p: ["M6.5 14.6V9.6h4l1.4 3.8", "M11.5 14.6H18.2v-3", "M8 10.2 3.6 6.2", "M3.6 6.2 2 7l.4 2.3 2.5.2"] },
		{ c: [[8, 18, 2], [13.5, 18, 2]], p: ["M3.4 9.4v6.1h11", "M14.4 15.5V9.4", "M1.5 12.4h1.9", "M15.6 8.6v8.2M17.4 8.6v8.2", "M19 9.4l1.4-.6M19.4 12.4H21M19 15.4l1.4.6"] },
		{ c: [[8, 18.2, 1.9], [16, 18.2, 1.9], [12, 10.6, 5.2], [12, 10.6, 2]], p: ["M6.9 12 2.4 13.4", "M2.4 13.4v1.5"] },
		{ c: [[8, 18, 1.9], [13.5, 18, 1.9]], p: ["M5.5 15.4V10a1.6 1.6 0 0 1 1.6-1.6h5.3A1.6 1.6 0 0 1 14 10v5.4", "M5.5 15.4h9", "M3.6 12.4H5.5", "M2 6.6h20", "M4 6.6v1.8M7 6.6v1.8M13 6.6v1.8M16 6.6v1.8M20 6.6v1.8"] },
		{ c: [[6.5, 18, 1.9], [17.5, 18, 1.9]], p: ["M4.5 8.6h15l-1.8 5.4H6.3z", "M6.3 8.6 7.3 6.6h9.4l1 2", "M4 14.4h16", "M6 14.4v2.7M9 14.4v3.1M12 14.4v2.7M15 14.4v3.1M18 14.4v2.7"] },
		{ c: [[7, 17.6, 2.4], [16.5, 17.6, 2.4]], p: ["M4.4 15.2V10h4.4v5.2", "M5.6 11h2v2.1h-2z", "M8.8 15.2h8.9v-3", "M6.9 10.4 20.4 6", "M20.4 6 19.1 8.9", "M18.3 8.9h3M18.8 8.9v2.5M20.9 8.4v2.5"] },
		{ c: [[12.7, 10.3, 1.3], [5.3, 18.3, 1.5]], p: ["M12.7 10.3C12.7 6.8 14.4 4.9 17.4 4.8", "M12.7 10.3C16.2 10.3 18.1 12 18.2 15", "M12.7 10.3C12.7 13.8 11 15.7 8 15.8", "M12.7 10.3C9.2 10.3 7.3 8.6 7.2 5.6", "M7.9 13.6 3.6 15.2", "M7.9 13.6 6.1 16.9"] }
	];
	var HAS_ROUND = ("roundRect" in Path2D.prototype);
	function iconPath(ic) {
		if (ic._P) return ic._P;
		var P = new Path2D(), i;
		if (ic.c) for (i = 0; i < ic.c.length; i++) { var x = ic.c[i][0], y = ic.c[i][1], r = ic.c[i][2]; P.moveTo(x + r, y); P.arc(x, y, r, 0, 7); }
		if (ic.q && HAS_ROUND) for (i = 0; i < ic.q.length; i++) { var q = ic.q[i]; P.roundRect(q[0], q[1], q[2], q[3], q[4]); }
		if (ic.p) for (i = 0; i < ic.p.length; i++) P.addPath(new Path2D(ic.p[i]));
		ic._P = P; return P;
	}

	/* ---- scatter layout: an aspect-fitted grid of <=16 cells, one distinct
	 *      glyph per cell (so a sheet never repeats a machine). Stable per load
	 *      and per resize, seeded once. -------------------------------------- */
	function rng(s) { s >>>= 0; return function () { s = (s + 0x6D2B79F5) | 0; var t = Math.imul(s ^ (s >>> 15), 1 | s); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; }; }
	function layoutIcons(w, h, seed) {
		var rnd = rng(seed), best = null, c, r, n, sc;
		for (c = 2; c <= 6; c++) for (r = 2; r <= 6; r++) {
			n = c * r; if (n > 16) continue;
			sc = Math.abs(Math.log((w / c) / (h / r)));
			if (!best || sc < best.sc - 1e-9 || (Math.abs(sc - best.sc) < 0.2 && n > best.n)) best = { c: c, r: r, n: n, sc: sc };
		}
		var cols = best.c, rows = best.r, idx = [], i, j;
		for (i = 0; i < 16; i++) idx.push(i);
		for (i = 15; i > 0; i--) { j = Math.floor(rnd() * (i + 1)); var tmp = idx[i]; idx[i] = idx[j]; idx[j] = tmp; }
		var items = [], cw = w / cols, ch = h / rows, base = Math.max(26, Math.min(64, Math.min(cw, ch) * 0.46)), k = 0, ry, cx;
		for (ry = 0; ry < rows; ry++) for (cx = 0; cx < cols; cx++) {
			items.push({
				x: cw * (cx + 0.5) + (rnd() - 0.5) * cw * 0.34,
				y: ch * (ry + 0.5) + (rnd() - 0.5) * ch * 0.34,
				s: base * (0.82 + rnd() * 0.36), rot: (rnd() - 0.5) * 0.09, idx: idx[k++]
			});
		}
		return items;
	}

	/* Draw the scatter: each glyph debossed into the sheet; the travelling light
	 * band lifts whichever column it sweeps. marks = orange mini-dimension line. */
	function scatterIcons(w, h, t, p, items, ink, marks) {
		var sx = ((t * 0.14) % 1.4 - 0.2) * w, band = w * 0.16, orange = RGB(p.orange), i, it, lit, P, k;
		ctx.lineJoin = "round"; ctx.lineCap = "round";
		for (i = 0; i < items.length; i++) {
			it = items[i]; lit = Math.max(0, 1 - Math.abs(it.x - sx) / band); P = iconPath(ICONS[it.idx]); k = 24 / it.s;
			ctx.save(); ctx.translate(it.x, it.y); ctx.rotate(it.rot); ctx.scale(it.s / 24, it.s / 24); ctx.translate(-12, -12);
			if (marks) {
				ctx.globalAlpha = 0.2 + lit * 0.34; ctx.strokeStyle = orange; ctx.lineWidth = k;
				ctx.beginPath(); ctx.moveTo(3, 22); ctx.lineTo(3, 23.3); ctx.moveTo(21, 22); ctx.lineTo(21, 23.3); ctx.moveTo(3, 22.65); ctx.lineTo(21, 22.65); ctx.stroke();
			}
			ctx.globalAlpha = 0.1 + lit * 0.17; ctx.strokeStyle = ink; ctx.lineWidth = 1.5 * k; ctx.stroke(P);
			if (lit > 0.02) { ctx.globalAlpha = lit * 0.5; ctx.strokeStyle = "#ffffff"; ctx.save(); ctx.translate(-0.7, -0.7); ctx.stroke(P); ctx.restore(); }
			ctx.restore();
		}
		ctx.globalAlpha = 1;
	}
	function sheen(w, h, t) {
		var sx = ((t * 0.14) % 1.4 - 0.2) * w, g = ctx.createLinearGradient(sx - 90, 0, sx + 90, 0);
		g.addColorStop(0, "rgba(255,255,255,0)"); g.addColorStop(0.5, "rgba(255,255,255,.09)"); g.addColorStop(1, "rgba(255,255,255,0)");
		ctx.fillStyle = g; ctx.fillRect(0, 0, w, h);
	}

	/* ---- the two sheets -------------------------------------------------- */
	function graph(w, h, t, p, items) {
		ctx.fillStyle = RGB(p.paper); ctx.fillRect(0, 0, w, h);
		var x, y;
		ctx.strokeStyle = mix(p.paper, p.green, 0.09); ctx.lineWidth = 1; ctx.beginPath();
		for (x = 0; x < w; x += 6) { ctx.moveTo(x + 0.5, 0); ctx.lineTo(x + 0.5, h); }
		for (y = 0; y < h; y += 6) { ctx.moveTo(0, y + 0.5); ctx.lineTo(w, y + 0.5); } ctx.stroke();
		ctx.strokeStyle = mix(p.paper, p.green, 0.2); ctx.beginPath();
		for (x = 0; x < w; x += 30) { ctx.moveTo(x + 0.5, 0); ctx.lineTo(x + 0.5, h); }
		for (y = 0; y < h; y += 30) { ctx.moveTo(0, y + 0.5); ctx.lineTo(w, y + 0.5); } ctx.stroke();
		scatterIcons(w, h, t, p, items, mix(p.ink, p.green, 0.35), true);
		sheen(w, h, t);
	}
	function hatch(w, h, t, p, items) {
		ctx.fillStyle = RGB(p.paper); ctx.fillRect(0, 0, w, h);
		var y;
		ctx.strokeStyle = mix(p.paper, p.dim, 0.16); ctx.lineWidth = 1;
		for (y = -w; y < h; y += 7) { ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(w, y + w); ctx.stroke(); }
		ctx.globalAlpha = 0.5;
		for (y = -w; y < h; y += 7) { ctx.beginPath(); ctx.moveTo(0, y + w); ctx.lineTo(w, y); ctx.stroke(); }
		ctx.globalAlpha = 1;
		scatterIcons(w, h, t, p, items, RGB(p.ink), false);
		sheen(w, h, t);
	}

	/* ---- driver ---------------------------------------------------------- */
	var DPR = Math.min(1.75, window.devicePixelRatio || 1);
	// A hard reload bypasses the service worker, so the page loads uncontrolled —
	// the one signal that separates it from an F5. Without a SW (plain-HTTP IP
	// origin) fall back to once-per-session. Computed ONCE: the sheet and the icon
	// scatter have to agree on what counts as a hard load, and the sessionStorage
	// fallback is a one-shot marker a second caller would already find set.
	// A missing controller alone is NOT proof of a hard reload: on a first visit,
	// and whenever registration is blocked (private window, blocked site data,
	// enterprise policy), there is never a controller and every plain F5 would look
	// "hard". Only trust the signal once the worker has actually controlled this
	// browser at least once; otherwise degrade to once-per-session, the same rule
	// used when the browser has no service worker at all.
	var HARD = (function () {
		var SEEN = "treckrr-sw-ctrl", SESS = "treckrr-loginbg-s";
		if ("serviceWorker" in navigator) {
			var controlled = false;
			try { controlled = !!navigator.serviceWorker.controller; } catch (x) { }
			if (controlled) { try { localStorage.setItem(SEEN, "1"); } catch (e) { } return false; }
			try { if (localStorage.getItem(SEEN) === "1") return true; } catch (e) { }
		}
		try {
			var fresh = !sessionStorage.getItem(SESS);
			if (fresh) sessionStorage.setItem(SESS, "1");
			return fresh;
		} catch (x) { return true; }
	})();
	// Sheet: stable on F5 / in-app navigation, re-rolled on a hard reload. Excludes
	// the last pick so a change always lands on the other sheet.
	function pickVariant(store, keys) {
		var last = null; try { last = localStorage.getItem(store); } catch (e) { }
		if (last && keys.indexOf(last) >= 0 && !HARD) return last;
		var pool = keys.filter(function (k) { return k !== last; }); if (!pool.length) pool = keys;
		var k = pool[Math.floor(Math.random() * pool.length)];
		try { localStorage.setItem(store, k); } catch (e) { }
		return k;
	}
	// Scatter seed: decides which glyph lands where, at what size and angle. It has
	// to follow the same rule as the sheet — a fresh seed per load would rearrange
	// every icon on a plain F5 while the sheet itself stayed put.
	function pickSeed(store) {
		var s = null; try { s = localStorage.getItem(store); } catch (e) { }
		if (!HARD && s && /^\d+$/.test(s)) return +s;
		var n = (Math.random() * 1e9) | 0;
		try { localStorage.setItem(store, String(n)); } catch (e) { }
		return n;
	}
	var draw = pickVariant("treckrr-loginbg", ["graph", "hatch"]) === "graph" ? graph : hatch;
	var seed = pickSeed("treckrr-loginbg-seed");
	var reduce = window.matchMedia ? matchMedia("(prefers-reduced-motion: reduce)") : { matches: false };
	// The light band is a pure function of t, so read t off the wall clock instead
	// of a random phase: a reload then picks the sweep up where the previous page
	// left it, rather than jumping the bar to a new position.
	// requestAnimationFrame is not guaranteed to keep firing. Chrome pauses it when
	// it decides the WINDOW is occluded — which a remote-desktop session routinely
	// triggers — and document.hidden stays false throughout, so visibilitychange
	// never fires and nothing notices. `running` is still true, so start() refuses
	// to do anything, and the loop is silently dead until the page is reloaded.
	//
	// Two changes make that survivable. The phase is read straight off the wall
	// clock rather than integrated from frame deltas, so any stall or throttle
	// resumes at the correct position instead of lagging behind by however long it
	// was paused. And a watchdog notices when frames stop arriving and drives the
	// drawing from a timer until they come back — timers keep running where rAF
	// does not. Because the animation is a pure function of the clock, the
	// timer-driven frames are indistinguishable from the real ones.
	var W = 0, H = 0, items = [], pal = palette(), running = false, raf = 0;
	var lastRaf = 0, fallback = 0;
	function clock() { return Date.now() / 1000; }

	function resize() {
		var r = canvas.getBoundingClientRect();
		if (!r.width || !r.height) return;
		W = r.width; H = r.height;
		canvas.width = Math.round(W * DPR); canvas.height = Math.round(H * DPR);
		ctx.setTransform(DPR, 0, 0, DPR, 0, 0);
		items = layoutIcons(W, H, seed);
		if (!running) draw(W, H, clock(), pal, items); // keep the static/paused frame current
	}
	function frame() {
		if (!running) return;
		// Stand the fallback down the instant a real frame arrives, rather than at
		// the watchdog's next tick: until then both would be drawing.
		if (fallback) { clearInterval(fallback); fallback = 0; }
		lastRaf = Date.now();
		draw(W, H, clock(), pal, items);
		raf = requestAnimationFrame(frame);
	}
	function start() {
		if (running || reduce.matches || document.hidden) return;
		running = true; lastRaf = Date.now(); raf = requestAnimationFrame(frame);
	}
	function stop() {
		running = false;
		if (raf) cancelAnimationFrame(raf); raf = 0;
		if (fallback) clearInterval(fallback); fallback = 0;
	}
	// lastRaf is stamped ONLY by frame(), never by the fallback, so the fallback
	// painting cannot mask a stall and keep itself alive.
	setInterval(function () {
		if (!running || reduce.matches || document.hidden) return;
		var stalled = Date.now() - lastRaf > 800;
		if (stalled && !fallback) fallback = setInterval(function () { draw(W, H, clock(), pal, items); }, 40);
		else if (!stalled && fallback) { clearInterval(fallback); fallback = 0; }
	}, 500);
	function retheme() { pal = palette(); if (!running) draw(W, H, clock(), pal, items); }

	if (window.ResizeObserver) new ResizeObserver(resize).observe(canvas);
	else window.addEventListener("resize", resize);
	resize();
	if (reduce.matches) draw(W, H, clock(), pal, items); else start();

	document.addEventListener("visibilitychange", function () { if (document.hidden) stop(); else start(); });
	if (reduce.addEventListener) reduce.addEventListener("change", function () { if (reduce.matches) stop(); else start(); });
	var dark = window.matchMedia ? matchMedia("(prefers-color-scheme: dark)") : null;
	if (dark && dark.addEventListener) dark.addEventListener("change", retheme);
	if (window.MutationObserver) new MutationObserver(retheme).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
})();
