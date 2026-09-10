// Offline booking capture: when a booking is submitted with no connection, it is
// queued in IndexedDB and replayed automatically when the connection returns.
// Each booking carries a client UUID (idempotency_key) so a double-replay can't
// double-book (the server's unique index makes the second insert a no-op). Works
// in every browser (no Background Sync dependency) — it flushes on the `online`
// event and on every page load while online.
(function () {
	"use strict";
	if (!("indexedDB" in window)) return;
	var DB = "treckrr-offline", STORE = "queue", VER = 1;

	function open() {
		return new Promise(function (res, rej) {
			var r = indexedDB.open(DB, VER);
			r.onupgradeneeded = function () {
				if (!r.result.objectStoreNames.contains(STORE)) r.result.createObjectStore(STORE, { keyPath: "id" });
			};
			r.onsuccess = function () { res(r.result); };
			r.onerror = function () { rej(r.error); };
		});
	}
	function put(item) {
		return open().then(function (db) {
			return new Promise(function (res, rej) {
				var t = db.transaction(STORE, "readwrite");
				t.objectStore(STORE).put(item);
				t.oncomplete = function () { res(); };
				t.onerror = function () { rej(t.error); };
			});
		});
	}
	function del(id) {
		return open().then(function (db) {
			return new Promise(function (res) {
				var t = db.transaction(STORE, "readwrite");
				t.objectStore(STORE).delete(id);
				t.oncomplete = function () { res(); };
				t.onerror = function () { res(); };
			});
		});
	}
	function all() {
		return open().then(function (db) {
			return new Promise(function (res) {
				var out = [], c = db.transaction(STORE).objectStore(STORE).openCursor();
				c.onsuccess = function (e) { var cur = e.target.result; if (cur) { out.push(cur.value); cur.continue(); } else res(out); };
				c.onerror = function () { res(out); };
			});
		});
	}
	function rememberRejection(id, status, message) {
		return open().then(function (db) {
			return new Promise(function (res, rej) {
				var t = db.transaction(STORE, "readwrite");
				var s = t.objectStore(STORE), r = s.get(id);
				var retained = false;
				r.onsuccess = function () {
					// The user may have discarded it while the request was in flight.
					// Update only the surviving item; never resurrect a stale snapshot.
					if (!r.result) return;
					var item = r.result;
					item.rejection = { status: status, message: message };
					s.put(item);
					retained = true;
				};
				t.oncomplete = function () { db.close(); res(retained); };
				t.onabort = t.onerror = function () { db.close(); rej(t.error); };
			});
		});
	}
	function correctRejected(item, fields) {
		return open().then(function (db) {
			return new Promise(function (res, rej) {
				var t = db.transaction(STORE, "readwrite"), s = t.objectStore(STORE), r = s.get(item.id);
				var saved = false;
				r.onsuccess = function () {
					var current = r.result;
					// Never resurrect a discarded/sent item or overwrite a newer edit.
					if (!current || !ownedByCurrentUser(current) || !current.rejection ||
						JSON.stringify(current.data) !== JSON.stringify(item.data)) return;
					if (!current.originalData) current.originalData = JSON.parse(JSON.stringify(current.data));
					if (current.data.__pairs) {
						fields.forEach(function (f) { current.data.__pairs[f.index][1] = f.input.value; });
					} else {
						fields.forEach(function (f) { current.data[f.key] = f.input.value; });
					}
					// Keep rejection: corrections require a deliberate "Jetzt senden".
					// Scope, row count and replay keys are never editable.
					s.put(current);
					saved = true;
				};
				t.oncomplete = function () { db.close(); res(saved); };
				t.onabort = t.onerror = function () { db.close(); rej(t.error); };
			});
		});
	}

	function uuid() { return crypto.randomUUID ? crypto.randomUUID() : (Date.now() + "-" + Math.random().toString(16).slice(2)); }
	function csrf() { var i = document.querySelector('input[name="csrf_token"]'); return i ? i.value : ""; }

	// app.js arms a double-submit lock on EVERY post form: it sets
	// dataset.submitting="1" and disables the button, and only a navigation (or a
	// bfcache restore) clears it. An offline capture prevents that navigation, so
	// without releasing the lock here the form would swallow every further submit
	// and the button would stay disabled — one offline booking per page load.
	// Mirrors entry-form.js's releaseButton(), which exists for the same reason.
	function releaseSubmitLock(form) {
		form.dataset.submitting = "";
		form.dataset.checking = "";
		// is-submitting is the marker app.js sets on whichever button it disabled
		// (the submitter, which need not carry type="submit"); the second selector
		// covers the plain case. Both are safe to re-enable — no submit button in
		// this app is disabled for any other reason.
		form.querySelectorAll("button.is-submitting, button[type='submit']").forEach(function (b) {
			b.classList.remove("is-submitting");
			b.removeAttribute("aria-busy");
			b.disabled = false;
		});
	}

	// The queue lives in origin-wide IndexedDB, not per user. On a shared browser
	// profile that meant a booking queued offline by one user was replayed — and
	// audited — under whoever happened to be logged in next. Stamping the owner at
	// queue time and matching it at flush time keeps each queue with its author.
	function currentUser() {
		var m = document.querySelector('meta[name="user-id"]');
		return m && m.content ? m.content : "";
	}
	// Strict ownership. An item with no owner stamp is QUARANTINED: it stays in the
	// queue but is never replayed and never counted, because there is no honest way
	// to decide whose booking it is. Letting the current user flush it — the earlier
	// transitional behaviour — reintroduces exactly the misattribution this owner
	// stamp exists to prevent, and the window is not as small as it looks: an item
	// that keeps failing to send (401, 5xx) lingers indefinitely, not just while the
	// device is offline. Quarantining keeps the data rather than dropping it.
	function ownedByCurrentUser(item) {
		return !!item.user && item.user === currentUser();
	}

	function badge(n) {
		var el = document.querySelector("[data-offline-badge]");
		if (!el) return;
		el.textContent = n ? (n + " offline") : "";
		el.hidden = !n;
	}
	function refreshBadge() {
		return all().then(function (a) { badge(a.filter(ownedByCurrentUser).length); });
	}

	function toast(msg) {
		var t = document.createElement("div");
		t.className = "toast toast--status"; t.setAttribute("role", "status");
		t.innerHTML = '<span class="toast__msg"></span>';
		t.firstChild.textContent = msg;
		document.body.appendChild(t);
		setTimeout(function () { t.remove(); }, 4000);
	}

	var flushing = null;
	function flush(retryRejected) {
		if (flushing) return flushing;
		if (!navigator.onLine) return Promise.resolve();
		// Returned so a caller can wait for the flush — the queue panel redraws
		// only once the sending is actually done.
		flushing = all().then(function (all_items) {
			var items = all_items.filter(ownedByCurrentUser);
			var orphans = all_items.length - items.length;
			if (orphans > 0 && window.console && console.warn) {
				console.warn("treckrr: " + orphans + " offline booking(s) without an owner are " +
					"held back and will not be sent automatically.");
			}
			if (!items.length) return;
			var token = csrf();
			return items.reduce(function (p, item) {
				return p.then(function () {
					// Validation failures need operator attention, not another automatic
					// attempt on every navigation. "Jetzt senden" explicitly retries them.
					if (item.rejection && !retryRejected) return;
					var body = new URLSearchParams();
					if (item.data && item.data.__pairs) {
						item.data.__pairs.forEach(function (p) { body.append(p[0], p[1]); });
					} else {
						Object.keys(item.data || {}).forEach(function (k) { body.set(k, item.data[k]); });
					}
					if (token) body.set("csrf_token", token);
					return fetch(item.path || "/entries", {
						method: "POST", credentials: "same-origin", redirect: "manual",
						// The server answers a replay with an explicit status (not a redirect):
						// 2xx = stored, 422/400 = needs attention, 401 = login needed.
						headers: { "Content-Type": "application/x-www-form-urlencoded", "X-Offline-Replay": "1" },
						body: body.toString()
					}).then(function (r) {
						if (r.status >= 200 && r.status < 300) return del(item.id); // stored (or already recorded)
						if (r.status === 422 || r.status === 400) {
							// A quick batch may already be partially saved. Keep ALL original
							// fields and keys: retrying them deduplicates the accepted rows.
							return r.text().catch(function () { return ""; }).then(function (message) {
								return rememberRejection(item.id, r.status, message.trim().slice(0, 1000));
							}).then(function (retained) {
								if (retained) toast("Offline-Buchung nicht vollständig gespeichert. Die Daten bleiben in der Warteschlange — bitte prüfen.");
							});
						}
						// 401 (session expired), 403 (stale CSRF), 5xx, an opaqueredirect
						// from an un-upgraded server, or a network error: keep it and retry.
					}).catch(function () { /* network died mid-flush: keep for next time */ });
				});
			}, Promise.resolve());
		}).then(function () { return refreshBadge(); }).then(function () {
			if (panel && !panel.hidden) return renderQueue();
		}).catch(function () {}).then(function () {
			flushing = null;
		});
		return flushing;
	}

	// Hook the booking form so an offline submit is queued instead of failing.
	var form = document.querySelector("[data-entry-form]");
	if (form && form.querySelector('[name="neighbor_id"]')) {
		if (!form.querySelector('[name="idempotency_key"]')) {
			var k = document.createElement("input");
			k.type = "hidden"; k.name = "idempotency_key"; k.value = uuid();
			form.appendChild(k);
		}
		// Registered before entry-form.js loads, so this runs first. At the event
		// TARGET the capture flag does not order listeners — registration order
		// does — and stopPropagation() would not stop the later listeners on this
		// same form either. stopImmediatePropagation() is what keeps
		// entry-form.js's precheck from running: its fetch fails offline and its
		// fail-open path calls requestSubmit(), which would queue this booking a
		// SECOND time under a freshly stamped key.
		form.addEventListener("submit", function (e) {
			if (navigator.onLine) return;
			e.preventDefault();
			e.stopImmediatePropagation();
			releaseSubmitLock(form);
			var data = {};
			new FormData(form).forEach(function (v, key) { if (key !== "csrf_token") data[String(key)] = v; });
			var id = data.idempotency_key || uuid();
			put({ id: id, data: data, user: currentUser() }).then(function () {
				var kf = form.querySelector('[name="idempotency_key"]');
				if (kf) kf.value = uuid();
				refreshBadge();
				toast("Offline gespeichert – wird bei Verbindung gesendet.");
			});
		}, true);
	}

	// ---- Schnellerfassung offline (Ausbaukarte 100) ------------------------
	// The idempotency infrastructure carried exactly one form. The quick-entry
	// table posts to /entries/quick and becomes N bookings, so each row gets its
	// OWN key — one key for the whole submit would let a replay create the first
	// booking and silently drop the rest.
	var quick = document.querySelector("[data-quick-form]");
	if (quick) {
		var stampKeys = function () {
			quick.querySelectorAll("[data-quick-key]").forEach(function (f) {
				if (!f.value) f.value = uuid();
			});
		};
		stampKeys();
		quick.addEventListener("submit", function (e) {
			if (navigator.onLine) return;
			e.preventDefault();
			e.stopImmediatePropagation();
			releaseSubmitLock(quick);
			stampKeys();
			var data = {};
			// Repeated field names must survive: FormData keeps every row, a plain
			// object would keep only the last one. The replay posts them back with
			// URLSearchParams, which handles repeats the same way.
			var pairs = [];
			new FormData(quick).forEach(function (v, key) {
				if (key !== "csrf_token") pairs.push([String(key), String(v)]);
			});
			data.__pairs = pairs;
			put({ id: uuid(), data: data, user: currentUser(), path: "/entries/quick" }).then(function () {
				quick.querySelectorAll("[data-quick-key]").forEach(function (f) { f.value = uuid(); });
				refreshBadge();
				toast("Offline gespeichert – wird bei Verbindung gesendet.");
			});
		}, true);
	}

	// ---- Warteschlange sichtbar machen (Ausbaukarte 72) --------------------
	// The badge was a dead counter: a booking that kept failing to send could
	// sit there indefinitely with no way to look at it, fix it or drop it.

	var panel = document.querySelector("[data-offline-panel]");
	var list = document.querySelector("[data-offline-list]");
	var badgeEl = document.querySelector("[data-offline-badge]");
	var panelLastFocus = null;
	var editableFields = {
		entry_date: "Datum", task_label: "Tätigkeit", hours: "Stunden", unit: "Einheit",
		quantity: "Menge", unit_price: "Einzelpreis", note: "Notiz",
		gespann_id: "Gespann-ID", person_id: "Person-ID (leer = ohne Helfer)",
		q_date: "Datum", q_hours: "Stunden", q_gespann: "Gespann-ID", q_person: "Person-ID (leer = ohne Helfer)"
	};
	function correctionEditor(item, pairs) {
		var details = document.createElement("details"), summary = document.createElement("summary");
		summary.textContent = "Buchungsdaten korrigieren";
		details.appendChild(summary);
		var hint = document.createElement("p");
		hint.className = "muted small";
		hint.textContent = "Nur abgelehnte Zeilen korrigieren. Bereits gespeicherte Zeilen bleiben unverändert. " +
			"Nachbar, Jahr und Buchungsschlüssel bleiben fest. Gespann-/Person-IDs stehen in den erfassten Daten; " +
			"eine leere Person-ID bucht ohne Helfer. Originaldaten bleiben über „Daten sichern“ verfügbar.";
		details.appendChild(hint);
		var fields = [], counts = {};
		pairs.forEach(function (pair, index) {
			var key = pair[0];
			if (!Object.prototype.hasOwnProperty.call(editableFields, key)) return;
			counts[key] = (counts[key] || 0) + 1;
			var label = document.createElement("label"), text = document.createElement("span"), input = document.createElement("input");
			label.className = "field";
			text.textContent = (item.data.__pairs ? "Zeile " + counts[key] + " · " : "") + editableFields[key];
			input.className = "input";
			input.type = "text";
			input.value = pair[1];
			label.appendChild(text);
			label.appendChild(input);
			details.appendChild(label);
			fields.push({ key: key, index: index, input: input });
		});
		var save = document.createElement("button");
		save.type = "button";
		save.className = "btn btn--ghost btn--sm";
		save.textContent = "Korrektur speichern";
		save.addEventListener("click", function () {
			save.disabled = true;
			Promise.resolve(flushing).then(function () { return correctRejected(item, fields); }).then(function (saved) {
				toast(saved ? "Korrektur gespeichert — mit „Jetzt senden“ erneut versuchen." :
					"Buchung inzwischen geändert oder gesendet. Bitte Warteschlange erneut prüfen.");
				return renderQueue();
			}).catch(function () {
				save.disabled = false;
				toast("Korrektur konnte nicht gespeichert werden. Die bisherigen Daten bleiben erhalten.");
			});
		});
		details.appendChild(save);
		return details;
	}

	// Build the text nodes with textContent, never innerHTML: every value here
	// is data the user typed into a booking form.
	function row(item) {
		var wrap = document.createElement("div");
		wrap.className = "offlineq__row";
		var meta = document.createElement("div");
		meta.className = "offlineq__meta";
		var d = item.data || {};
		var title = document.createElement("strong");
		var sub = document.createElement("span");
		sub.className = "muted small";
		if (d.__pairs) {
			// A quick-entry submit: several rows in one item, so it is described
			// by how many rows it carries rather than by one booking's fields.
			var rows = d.__pairs.filter(function (p) {
				return p[0] === "q_hours" && String(p[1]).trim() !== "";
			}).length;
			title.textContent = "Schnellerfassung · " + rows + " Zeile(n)";
			var firstDate = d.__pairs.find(function (p) { return p[0] === "q_date"; });
			sub.textContent = firstDate ? firstDate[1] : "ohne Datum";
		} else {
			var qty = d.unit && d.unit !== "h" ? (d.quantity || "?") + " " + d.unit : (d.hours || "?") + " h";
			title.textContent = (d.task_label || "Buchung") + " · " + qty;
			sub.textContent = (d.entry_date || "ohne Datum") + (d.note ? " · " + d.note : "");
		}
		meta.appendChild(title);
		meta.appendChild(sub);
		if (item.rejection) {
			var error = document.createElement("p");
			error.className = "small";
			error.textContent = "Bitte prüfen (HTTP " + item.rejection.status + "): " +
				(item.rejection.message || "Die Buchung wurde abgelehnt.") +
				" Die erfassten Daten bleiben erhalten. Unten korrigieren oder Stammdaten berichtigen, dann mit „Jetzt senden“ erneut versuchen. " +
				"Bereits gespeicherte Zeilen werden dabei nicht doppelt gebucht.";
			meta.appendChild(error);
		}
		var details = document.createElement("details");
		var summary = document.createElement("summary");
		summary.textContent = "Erfasste Daten anzeigen";
		var values = document.createElement("pre");
		values.className = "offlineq__data small";
		var fields = d.__pairs || Object.keys(d).map(function (key) { return [key, d[key]]; });
		values.textContent = fields.map(function (p) { return p[0] + ": " + p[1]; }).join("\n");
		details.appendChild(summary);
		details.appendChild(values);
		meta.appendChild(details);
		if (item.rejection) meta.appendChild(correctionEditor(item, fields));
		var actions = document.createElement("div");
		actions.className = "btnrow";
		var backup = document.createElement("button");
		backup.type = "button";
		backup.className = "btn btn--ghost btn--sm";
		backup.textContent = "Daten sichern";
		backup.addEventListener("click", function () {
			// An explicit local backup also preserves work if its helper no longer
			// exists. Exporting never removes the queue item or changes replay keys.
			var url = URL.createObjectURL(new Blob([JSON.stringify(item, null, 2)], { type: "application/json" }));
			var a = document.createElement("a");
			a.href = url;
			a.download = "treckrr-offline-" + String(item.id).replace(/[^a-zA-Z0-9-]/g, "_") + ".json";
			document.body.appendChild(a);
			a.click();
			a.remove();
			setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
		});
		var drop = document.createElement("button");
		drop.type = "button";
		drop.className = "btn btn--danger btn--sm";
		drop.textContent = "Verwerfen";
		drop.addEventListener("click", function () {
			if (!window.confirm("Diese offline erfasste Buchung verwerfen? Sie wird nicht gesendet.")) return;
			del(item.id).then(function () { renderQueue(); refreshBadge(); });
		});
		wrap.appendChild(meta);
		actions.appendChild(backup);
		actions.appendChild(drop);
		wrap.appendChild(actions);
		return wrap;
	}

	function renderQueue() {
		if (!list) return Promise.resolve();
		return all().then(function (items) {
			var mine = items.filter(ownedByCurrentUser);
			list.textContent = "";
			if (!mine.length) {
				var empty = document.createElement("p");
				empty.className = "muted small";
				empty.textContent = "Nichts in der Warteschlange.";
				list.appendChild(empty);
				return;
			}
			mine.forEach(function (item) { list.appendChild(row(item)); });
		});
	}

	function openPanel() {
		if (!panel) return;
		panelLastFocus = document.activeElement;
		renderQueue().then(function () {
			panel.hidden = false;
			var close = panel.querySelector("[data-offline-close]");
			if (close) close.focus();
		});
	}
	function closePanel() {
		if (!panel) return;
		panel.hidden = true;
		if (panelLastFocus && typeof panelLastFocus.focus === "function") panelLastFocus.focus();
		panelLastFocus = null;
	}

	if (badgeEl) badgeEl.addEventListener("click", openPanel);
	if (panel) {
		var closeBtn = panel.querySelector("[data-offline-close]");
		if (closeBtn) closeBtn.addEventListener("click", closePanel);
		var flushBtn = panel.querySelector("[data-offline-flush]");
		if (flushBtn) {
			flushBtn.addEventListener("click", function () {
				if (!navigator.onLine) { toast("Keine Verbindung — die Buchungen bleiben gespeichert."); return; }
				flush(true).then(function () { renderQueue(); });
			});
		}
		// Click on the backdrop (not the sheet) and Escape both close it.
		panel.addEventListener("click", function (e) { if (e.target === panel) closePanel(); });
		document.addEventListener("keydown", function (e) {
			if (panel.hidden) return;
			if (e.key === "Escape") { e.preventDefault(); closePanel(); }
			else if (window.TreckrrDialog) window.TreckrrDialog.trapFocus(panel, e);
		});
	}

	window.addEventListener("online", function () { flush(); });
	refreshBadge();
	flush();
})();
