/* App-wide worksheet backdrop.
 *
 * Paints one of three flüsterleise Werkblatt surfaces onto the fixed
 * <canvas id="app-bg"> that sits behind every authenticated page. Which one is
 * chosen at random on each load, the way the appbar picks a random machine mark:
 *
 *   werkraster — the app's 26px blueprint grid with faint nodes; now and then a
 *                soft green pulse expands from a node.
 *   taglicht   — no marks; a very soft warm/cool wash whose light drifts like the
 *                sun. The quietest surface.
 *   saatraster — a fine seed-dot grid; a diagonal drill pass dabs a few dots green.
 *
 * The surface is deliberately barely-there (3–9% contrast) so it disappears
 * behind tables and forms all day; .main is transparent, so it shows only in the
 * content gutters, with the frosted topstack, the drawer and the opaque cards
 * sitting above it. Colours are read from the live design tokens (--bg/--text/
 * --primary/--accent), so it tracks Hell/Nachtschicht and re-themes on the fly.
 *
 * Good-citizen rendering: the static layer (grid / dots) is drawn once to an
 * offscreen canvas and blitted each frame; only the pulse / sun / drill overlay
 * animates. Honours prefers-reduced-motion (a single static frame, no loop),
 * pauses when the tab is hidden, DPR-capped, opaque context. Purely decorative:
 * aria-hidden, pointer-inert; degrades to the plain --bg background when JS is
 * off or the 2D context is unavailable. CSP-safe — no inline code.
 */
(function () {
	"use strict";
	var canvas = document.getElementById("app-bg");
	if (!canvas) return;
	var ctx = canvas.getContext("2d", { alpha: false });
	if (!ctx) return;

	/* ---- colour helpers (tokens are #rrggbb; tolerate rgb()/#rgb) --------- */
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
			bg: tok("--bg", "#edf1ec"), ink: tok("--text", "#19211d"),
			green: tok("--primary", "#115638"), gold: tok("--accent", "#c8891f")
		};
	}

	/* ---- the three surfaces ---------------------------------------------- *
	 * build(o,w,h,p)      → draw the cacheable static layer onto offscreen ctx o
	 * overlay(c,w,h,t,dt,p) → draw the animated layer onto the main ctx (after blit)
	 * reset()             → clear animation state (called once at start)          */
	var VARIANTS = {
		werkraster: (function () {
			var pulses = [], acc = 0;
			return {
				reset: function () { pulses = []; acc = 0; },
				build: function (o, w, h, p) {
					o.fillStyle = RGB(p.bg); o.fillRect(0, 0, w, h);
					var g = 26, x, y;
					o.strokeStyle = mix(p.bg, p.ink, 0.06); o.lineWidth = 1; o.beginPath();
					for (x = g; x < w; x += g) { o.moveTo(x + 0.5, 0); o.lineTo(x + 0.5, h); }
					for (y = g; y < h; y += g) { o.moveTo(0, y + 0.5); o.lineTo(w, y + 0.5); } o.stroke();
					o.fillStyle = mix(p.bg, p.ink, 0.09);
					for (x = g; x < w; x += g) for (y = g; y < h; y += g) o.fillRect(x - 0.6, y - 0.6, 1.6, 1.6);
				},
				overlay: function (c, w, h, t, dt, p) {
					var g = 26, i, s;
					acc += dt;
					if (acc > 3.4) { acc = 0; pulses.push({ x: Math.round((Math.random() * (w - 2 * g) + g) / g) * g, y: Math.round((Math.random() * (h - 2 * g) + g) / g) * g, r: 2, a: 0.5 }); }
					for (i = 0; i < pulses.length; i++) {
						s = pulses[i]; s.r += 17 * dt; s.a -= dt * 0.2;
						if (s.a > 0) { c.strokeStyle = mix(p.bg, p.green, s.a); c.lineWidth = 1; c.beginPath(); c.arc(s.x, s.y, s.r, 0, 7); c.stroke(); }
					}
					pulses = pulses.filter(function (d) { return d.a > 0; });
				}
			};
		})(),
		taglicht: {
			reset: function () { },
			build: function (o, w, h, p) { o.fillStyle = RGB(p.bg); o.fillRect(0, 0, w, h); },
			overlay: function (c, w, h, t, dt, p) {
				var cx = w * (0.5 + Math.sin(t * 0.02) * 0.28), cy = h * 0.08;
				var g = c.createRadialGradient(cx, cy, 10, cx, cy, Math.max(w, h) * 0.95);
				g.addColorStop(0, mix(p.bg, p.gold, 0.07)); g.addColorStop(0.5, mix(p.bg, p.gold, 0.02)); g.addColorStop(1, mix(p.bg, p.green, 0.03));
				c.fillStyle = g; c.fillRect(0, 0, w, h);
			}
		},
		saatraster: (function () {
			var pass = -0.3;
			return {
				reset: function () { pass = -0.3; },
				build: function (o, w, h, p) {
					o.fillStyle = RGB(p.bg); o.fillRect(0, 0, w, h);
					var g = 22, x, y; o.fillStyle = mix(p.bg, p.ink, 0.07);
					for (x = g; x < w; x += g) for (y = g; y < h; y += g) { o.beginPath(); o.arc(x, y, 1, 0, 7); o.fill(); }
				},
				overlay: function (c, w, h, t, dt, p) {
					var g = 22, x, y, d; pass += dt * 0.05; if (pass > 1.35) pass = -0.3;
					var px = pass * w * 1.4; c.fillStyle = mix(p.bg, p.green, 0.5);
					for (x = g; x < w; x += g) for (y = g; y < h; y += g) {
						d = Math.abs((x + y * 0.4) - px);
						if (d < 28) { c.globalAlpha = (1 - d / 28) * 0.4; c.beginPath(); c.arc(x, y, 1.5, 0, 7); c.fill(); }
					}
					c.globalAlpha = 1;
				}
			};
		})()
	};
	// Keep the surface stable on F5 / in-app navigation and re-roll it only on a
	// hard reload — which bypasses the service worker, so the page loads with no
	// controller (the one signal that separates it from an F5). Without a SW
	// (plain-HTTP IP origin) fall back to once-per-session. Excludes the previous
	// pick so a hard reload always lands on a different surface, cycling through
	// the three over successive hard reloads.
	function pickVariant(store, keys) {
		var hard;
		if ("serviceWorker" in navigator) { try { hard = !navigator.serviceWorker.controller; } catch (x) { hard = true; } }
		else { try { hard = !sessionStorage.getItem(store + "-s"); sessionStorage.setItem(store + "-s", "1"); } catch (x) { hard = true; } }
		var last = null; try { last = localStorage.getItem(store); } catch (e) { }
		if (last && keys.indexOf(last) >= 0 && !hard) return last;
		var pool = keys.filter(function (k) { return k !== last; }); if (!pool.length) pool = keys;
		var k = pool[Math.floor(Math.random() * pool.length)];
		try { localStorage.setItem(store, k); } catch (e) { }
		return k;
	}
	var KEYS = ["werkraster", "taglicht", "saatraster"];
	var v = VARIANTS[pickVariant("treckrr-appbg", KEYS)];

	/* ---- driver: cached static layer + animated overlay ------------------ */
	var DPR = Math.min(1.5, window.devicePixelRatio || 1);
	var stat = document.createElement("canvas"), sctx = stat.getContext("2d", { alpha: false });
	var reduce = window.matchMedia ? matchMedia("(prefers-reduced-motion: reduce)") : { matches: false };
	// t is read off the wall clock rather than a random phase, so the travelling
	// light / sun / drill picks up where the previous page left it instead of
	// jumping on a reload that deliberately kept the same surface.
	var W = 0, H = 0, pal = palette(), t = Date.now() / 1000, prev = 0, running = false, raf = 0;
	// Whether the visible canvas has ever been composed. The ground is only handed
	// over to it (see markActive) once this is true.
	var painted = false;

	// The canvas sits at z-index:-1, which paints BELOW body's own background — so
	// body's opaque --bg fill and its blueprint grid would hide it completely.
	// Hand the paper over to the canvas only once we can actually paint: this
	// class makes body transparent (see .has-appbg in app.css), while no-JS and
	// no-2d-context keep the static CSS grid as the fallback.
	function markActive() { try { document.documentElement.classList.add("has-appbg"); } catch (e) { } }

	function size() {
		var r = canvas.getBoundingClientRect();
		if (!r.width || !r.height) return false;
		W = r.width; H = r.height;
		var cw = Math.round(W * DPR), ch = Math.round(H * DPR);
		// Assigning canvas.width/height runs "set bitmap dimensions", which clears
		// the bitmap — and for an {alpha:false} context it clears to OPAQUE BLACK —
		// even when the value is unchanged. So only touch it on a real size change
		// (that also skips a full rebuild per ResizeObserver tick).
		var resized = cw !== canvas.width || ch !== canvas.height;
		if (resized) {
			canvas.width = stat.width = cw;
			canvas.height = stat.height = ch;
		}
		// Repaint whenever the bitmap was just cleared, and also when nothing has
		// ever been painted: a first layout of exactly the default bitmap size
		// (300x150 at DPR 1) leaves `resized` false, and skipping the paint there
		// would hand the ground over to a canvas still in its opaque-black
		// initial state.
		if (resized || !painted) {
			sctx.setTransform(DPR, 0, 0, DPR, 0, 0);
			v.build(sctx, W, H, pal);
			compose(0);
			painted = true;
		}
		markActive(); // unreachable until painted === true
		return true;
	}
	function compose(dt) {
		ctx.setTransform(1, 0, 0, 1, 0, 0); ctx.drawImage(stat, 0, 0);
		ctx.setTransform(DPR, 0, 0, DPR, 0, 0);
		v.overlay(ctx, W, H, t, dt, pal);
	}
	function frame(now) {
		if (!running) return;
		var dt = Math.min(0.05, (now - prev) / 1000 || 0.016); prev = now; t += dt;
		compose(dt); raf = requestAnimationFrame(frame);
	}
	function start() { if (running || reduce.matches || document.hidden || !W) return; running = true; prev = performance.now(); raf = requestAnimationFrame(frame); }
	function stop() { running = false; if (raf) cancelAnimationFrame(raf); raf = 0; }
	function retheme() { pal = palette(); if (W) { v.build(sctx, W, H, pal); if (!running) compose(0); } }

	v.reset();
	// size() paints the first frame itself, so a hidden load (background tab,
	// session restore, prerender) never sits on an unpainted canvas. start() is
	// idempotent, and the resize paths call it too so a first size() that failed
	// (canvas not laid out yet) still gets the loop going once it succeeds.
	if (size() && !reduce.matches) start();

	if (window.ResizeObserver) new ResizeObserver(function () { if (size() && !reduce.matches) start(); }).observe(canvas);
	else window.addEventListener("resize", function () { if (size() && !reduce.matches) start(); });
	document.addEventListener("visibilitychange", function () { if (document.hidden) stop(); else start(); });
	if (reduce.addEventListener) reduce.addEventListener("change", function () { if (reduce.matches) { stop(); compose(0); } else start(); });
	var dark = window.matchMedia ? matchMedia("(prefers-color-scheme: dark)") : null;
	if (dark && dark.addEventListener) dark.addEventListener("change", retheme);
	if (window.MutationObserver) new MutationObserver(retheme).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
})();
