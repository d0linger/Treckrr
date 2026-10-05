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
			// Werkblatt: Manrope is retired — headings render in JetBrains Mono,
			// so the export inlines only the faces the page actually uses.
			var faces = [
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
			// cssText, not setAttribute("style", …): the latter writes a style
			// ATTRIBUTE, which style-src 'self' blocks, so every element in the clone
			// came out unstyled and the exported PNG lost its layout. Assigning through
			// the CSSOM is not governed by CSP and applies normally.
			dst.style.cssText = s;
			var sc = src.children, dc = dst.children;
			for (var j = 0; j < sc.length; j++) inlineStyles(sc[j], dc[j]);
		}
		function belegPng() {
			var rect = beleg.getBoundingClientRect();
			// Use the full CONTENT size, not the viewport-constrained box: on a narrow
			// screen the beleg's rows/summary line can be wider than what's visible, and
			// sizing the canvas to rect.width alone clips everything on the right.
			var w = Math.ceil(Math.max(rect.width, beleg.scrollWidth));
			// Measure the height AND capture the computed styles at the SAME width the
			// clone will render at: forcing a wider width reflows the content (grid
			// tracks, wrap points, height), so both must be read while the widened layout
			// is applied. Briefly apply the width, then clone + inline styles + measure
			// inside that window; try/finally guarantees the live element is restored even
			// if cloning throws.
			var prevW = beleg.style.width, prevMax = beleg.style.maxWidth, prevOv = beleg.style.overflow;
			var h, clone;
			try {
				beleg.style.maxWidth = "none"; beleg.style.overflow = "visible"; beleg.style.width = w + "px";
				h = Math.ceil(beleg.scrollHeight);
				clone = beleg.cloneNode(true);
				inlineStyles(beleg, clone);
			} finally {
				beleg.style.width = prevW; beleg.style.maxWidth = prevMax; beleg.style.overflow = prevOv;
			}
			// The live .beleg clips with overflow:hidden and is capped by max-width; in the
			// foreignObject those would shave the right edge off. Let the clone size to the
			// full captured width and show everything so no column is cut.
			// Each override sets the LOGICAL twin alongside the physical name.
			// getComputedStyle enumerates both and inlineStyles copies the lot in that
			// order, so whichever lands later in the declaration wins — inline-size sits
			// after width, and margin-inline after margin. Zeroing only marginLeft and
			// marginRight therefore left the beleg's centring margin (210px at this
			// width) in force: the clone rendered that far to the right and carried its
			// right-hand column off the canvas.
			clone.style.setProperty("overflow", "visible");
			["max-width", "max-inline-size"].forEach(function (k) { clone.style.setProperty(k, "none"); });
			["width", "inline-size"].forEach(function (k) { clone.style.setProperty(k, w + "px"); });
			["margin-left", "margin-right", "margin-inline"].forEach(function (k) { clone.style.setProperty(k, "0"); });
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
						// Ground for the exported PNG. On authenticated pages the backdrop
						// canvas owns the ground and body is transparent (.has-appbg in
						// app.css), so fall through to <html>, which still carries --bg —
						// otherwise a Nachtschicht Beleg would export on white. Plain
						// white stays the last resort.
						var opaqueBg = function (c) { return c && c !== "transparent" && !/rgba?\(\s*0\s*,\s*0\s*,\s*0\s*,\s*0\s*\)/.test(c); };
						var bg = getComputedStyle(document.body).backgroundColor;
						if (!opaqueBg(bg)) bg = getComputedStyle(document.documentElement).backgroundColor;
						if (!opaqueBg(bg)) bg = "#fff";
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

		var calculationBtn = scope.querySelector("[data-beleg-calculation]");
		if (calculationBtn) {
			scope.classList.add("is-calculation-collapsible");
			calculationBtn.addEventListener("click", function () {
				var on = !beleg.classList.contains("beleg--calculation");
				beleg.classList.toggle("beleg--calculation", on);
				calculationBtn.setAttribute("aria-pressed", on ? "true" : "false");
			});
		}

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

