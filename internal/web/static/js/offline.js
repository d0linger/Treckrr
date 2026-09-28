/**
 * Queues offline bookings in IndexedDB and attempts replay on page load and the
 * online event, without Background Sync. Client UUIDs let the server deduplicate
 * repeated submissions. Browsers without IndexedDB skip this enhancement.
 */
(function () {
	"use strict";
	if (!("indexedDB" in window)) return;
	var DB = "treckrr-offline", STORE = "queue", VER = 1;
	// One sender per origin: two tabs flushing the same snapshot would both POST
	// an item one of them has just discarded or corrected.
	var LOCK = "treckrr-offline-queue";
	// A POST that neither answers nor fails is a dead connection that the browser
	// still reports as online (weak field signal, captive portal).
	var SEND_TIMEOUT = 20000;
	// 403/409/redirect answers are not validation failures, but they do not heal
	// on their own either (read-only account, forced password change).
	var SOFT_FAILURE_LIMIT = 3;
	var DAY = 24 * 60 * 60 * 1000, QUARANTINE_DAYS = 30;

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
	/**
	 * Reads the CURRENT item in a readwrite transaction and lets change() mutate
	 * it; returning false skips the write. Resolves with the item, or null when it
	 * was discarded meanwhile — a stale snapshot is never resurrected.
	 */
	function update(id, change) {
		return open().then(function (db) {
			return new Promise(function (res, rej) {
				var t = db.transaction(STORE, "readwrite"), s = t.objectStore(STORE), r = s.get(id), out = null;
				r.onsuccess = function () {
					if (!r.result) return;
					out = r.result;
					if (change(out) !== false) s.put(out);
				};
				t.oncomplete = function () { db.close(); res(out); };
				t.onabort = t.onerror = function () { db.close(); rej(t.error); };
			});
		});
	}
	function rememberRejection(id, rejection) {
		return update(id, function (item) { item.rejection = rejection; }).then(function (item) { return !!item; });
	}
	/** Counts a non-validation refusal; resolves true once it needs attention. */
	function noteSoftFailure(id, status, message) {
		var flagged = false;
		return update(id, function (item) {
			item.softFailures = (item.softFailures || 0) + 1;
			if (item.softFailures < SOFT_FAILURE_LIMIT || item.rejection) return;
			item.rejection = { status: status, message: message };
			flagged = true;
		}).then(function () { return flagged; });
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
					// Scope, row count, saved rows and replay keys are never editable.
					s.put(current);
					saved = true;
				};
				t.oncomplete = function () { db.close(); res(saved); };
				t.onabort = t.onerror = function () { db.close(); rej(t.error); };
			});
		});
	}

	/**
	 * Runs task under the origin-wide queue lock (Web Locks). With ifAvailable the
	 * task receives null while another tab holds it. Browsers without Web Locks
	 * keep the previous per-tab behaviour.
	 */
	function withQueueLock(options, task) {
		if (navigator.locks && typeof navigator.locks.request === "function") {
			return navigator.locks.request(LOCK, options, task);
		}
		return Promise.resolve(true).then(task);
	}

	/** fetch() with an upper bound, so a silent connection counts as a failure. */
	function timedFetch(url, init) {
		if (typeof AbortController !== "function") return fetch(url, init);
		var ctrl = new AbortController(), timer = setTimeout(function () { ctrl.abort(); }, SEND_TIMEOUT);
		init.signal = ctrl.signal;
		return fetch(url, init).then(function (r) { clearTimeout(timer); return r; },
			function (err) { clearTimeout(timer); throw err; });
	}

	function uuid() { return crypto.randomUUID ? crypto.randomUUID() : (Date.now() + "-" + Math.random().toString(16).slice(2)); }
	function csrf() { var i = document.querySelector('input[name="csrf_token"]'); return i ? i.value : ""; }
	/** Local calendar date (not toISOString(), which shifts to UTC). */
	function localDate(d) {
		return d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
	}

	// app.js arms a double-submit lock on EVERY post form: it sets
	// dataset.submitting="1" and disables the button, and only a navigation (or a
	// bfcache restore) clears it. An offline capture prevents that navigation, so
	// without releasing the lock here the form would swallow every further submit
	// and the button would stay disabled — one offline booking per page load.
	// Mirrors entry-form.js's releaseButton(), which exists for the same reason.
	// dataset.checked is entry-form.js's "precheck passed" marker: a booking that
	// ends up queued instead of posted must run the precheck again next time.
	function releaseSubmitLock(form) {
		form.dataset.submitting = "";
		form.dataset.checking = "";
		form.dataset.checked = "";
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

	/**
	 * Retention for a shared device: an item whose owner has not used this browser
	 * for QUARANTINE_DAYS (and an unowned legacy item that long after it was first
	 * seen) is deleted. The current user's own items are never expired — they are
	 * visible in the queue panel and only the user discards them.
	 */
	function maintain() {
		var me = currentUser(), now = Date.now();
		if (!me) return Promise.resolve();
		return open().then(function (db) {
			return new Promise(function (res) {
				var t = db.transaction(STORE, "readwrite"), c = t.objectStore(STORE).openCursor();
				c.onsuccess = function (e) {
					var cur = e.target.result;
					if (!cur) return;
					var item = cur.value;
					if (item.user === me) {
						if (!item.ownerSeen || now - item.ownerSeen > DAY) { item.ownerSeen = now; cur.update(item); }
					} else {
						var since = item.ownerSeen || item.created || item.quarantinedAt;
						if (!since) { item.quarantinedAt = now; cur.update(item); }
						else if (now - since > QUARANTINE_DAYS * DAY) cur.delete();
					}
					cur.continue();
				};
				t.oncomplete = function () { db.close(); res(); };
				t.onabort = t.onerror = function () { db.close(); res(); };
			});
		}).catch(function () {});
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

	/** Reads a replay answer: JSON ({message, rows}) from this server, else text. */
	function readReply(r) {
		if (r.type === "opaqueredirect") return Promise.resolve({ message: "" });
		var json = (r.headers.get("Content-Type") || "").indexOf("application/json") === 0;
		return (json ? r.json() : r.text().then(function (text) { return { message: text }; }))
			.catch(function () { return { message: "" }; })
			.then(function (reply) {
				reply = reply && typeof reply === "object" ? reply : {};
				return { message: String(reply.message || "").trim().slice(0, 1000), rows: reply.rows && typeof reply.rows === "object" ? reply.rows : null };
			});
	}

	var REDIRECT_MESSAGE = "Der Server nimmt die Buchung derzeit nicht an (z. B. Nur-Lese-Konto oder Passwortänderung erforderlich). Bitte Anmeldung und Berechtigung prüfen.";

	function send(item, token) {
		var body = new URLSearchParams();
		if (item.data && item.data.__pairs) {
			item.data.__pairs.forEach(function (p) { body.append(p[0], p[1]); });
		} else {
			Object.keys(item.data || {}).forEach(function (k) { body.set(k, item.data[k]); });
		}
		if (token) body.set("csrf_token", token);
		return timedFetch(item.path || "/entries", {
			method: "POST", credentials: "same-origin", redirect: "manual",
			// The server answers a replay with an explicit status (not a redirect):
			// 2xx = stored, 422/400 = needs attention, 401 = login needed.
			headers: {
				"Content-Type": "application/x-www-form-urlencoded", "X-Offline-Replay": "1",
				"Accept": "application/json, text/plain;q=0.9"
			},
			body: body.toString()
		}).then(function (r) {
			if (r.status >= 200 && r.status < 300) return del(item.id); // stored (or already recorded)
			if (r.status === 422 || r.status === 400) {
				// A quick batch may already be partially saved. Keep ALL original
				// fields and keys: retrying them deduplicates the accepted rows.
				return readReply(r).then(function (reply) {
					var rejection = { status: r.status, message: reply.message };
					// Already stored under this key, with different data: a correction
					// can never be sent, and re-entering it would bill twice.
					if (r.headers.get("X-Treckrr-Replay") === "stored") rejection.stored = true;
					if (reply.rows) rejection.rows = reply.rows;
					return rememberRejection(item.id, rejection).then(function (retained) {
						if (!retained) return;
						toast(rejection.stored ? "Offline-Buchung war bereits gespeichert (abweichend) — bitte in der Warteschlange prüfen." :
							"Offline-Buchung nicht vollständig gespeichert. Die Daten bleiben in der Warteschlange — bitte prüfen.");
					});
				});
			}
			if (r.type === "opaqueredirect" || r.status === 403 || r.status === 409) {
				return readReply(r).then(function (reply) {
					// A stale CSRF token heals with the next page load: retry silently.
					if (r.status === 403 && /CSRF/i.test(reply.message)) return false;
					return noteSoftFailure(item.id, r.status, reply.message || REDIRECT_MESSAGE);
				}).then(function (flagged) {
					if (flagged) toast("Offline-Buchung wird vom Server nicht angenommen — bitte in der Warteschlange prüfen.");
				});
			}
			// 401 (session expired), 5xx, or a network error: keep it and retry.
		}).catch(function () { /* network died mid-flush: keep for next time */ });
	}

	function sendAll(retryRejected) {
		return all().then(function (all_items) {
			var items = all_items.filter(ownedByCurrentUser);
			var orphans = all_items.length - items.length;
			if (orphans > 0 && window.console && console.warn) {
				console.warn("treckrr: " + orphans + " offline booking(s) without an owner are " +
					"held back and will not be sent automatically.");
			}
			if (!items.length) return;
			var token = csrf();
			return items.reduce(function (p, snapshot) {
				return p.then(function () {
					// Re-read right before sending: an item discarded or corrected since
					// the snapshot (here or in another tab) must not go out stale.
					return update(snapshot.id, function () { return false; }).then(function (item) {
						if (!item || !ownedByCurrentUser(item)) return;
						// Validation failures need operator attention, not another automatic
						// attempt on every navigation. "Jetzt senden" explicitly retries them.
						if (item.rejection && !retryRejected) return;
						return send(item, token);
					});
				});
			}, Promise.resolve());
		});
	}

	var flushing = null;
	/** Disables "Verwerfen" while a send is in flight (see flush). */
	function markSending(on) {
		if (!list) return;
		list.querySelectorAll("[data-offline-drop]").forEach(function (b) { b.disabled = on; });
	}
	function flush(retryRejected) {
		if (flushing) return flushing;
		if (!navigator.onLine) return Promise.resolve();
		markSending(true);
		// Returned so a caller can wait for the flush — the queue panel redraws
		// only once the sending is actually done.
		flushing = withQueueLock({ ifAvailable: true }, function (lock) {
			// Another tab is already sending this origin's queue.
			if (!lock) return;
			return sendAll(retryRejected);
		}).catch(function () {}).then(function () {
			flushing = null;
			return refreshBadge();
		}).then(function () {
			if (panel && !panel.hidden) return renderQueue();
			markSending(false);
		}).catch(function () {});
		return flushing;
	}

	/** Account page the server redirects a booking form to. */
	function accountURL(f) {
		var n = f.querySelector('[name="neighbor_id"]'), y = f.querySelector('[name="year_id"]');
		return "/neighbors/" + encodeURIComponent(n ? n.value : "") + (y && y.value ? "?year=" + encodeURIComponent(y.value) : "");
	}
	function rotateKeys(f) {
		f.querySelectorAll('[name="idempotency_key"], [data-quick-key]').forEach(function (k) { k.value = uuid(); });
	}
	/**
	 * Stores the form's successful controls in the queue. The capture time is kept
	 * and fills an empty date, so a replay days later never books "today" instead.
	 */
	function capture(f, path, message) {
		if (path === "/entries/quick") stampKeys();
		var capturedAt = new Date(), day = localDate(capturedAt);
		// Repeated field names must survive: FormData keeps every row and every
		// machine_id, a plain object would keep only the last one. The replay posts
		// them back with URLSearchParams, which handles repeats the same way.
		var pairs = [];
		new FormData(f).forEach(function (v, key) {
			if (key === "csrf_token") return;
			var value = String(v);
			if ((key === "entry_date" || key === "q_date") && !value.trim()) value = day;
			pairs.push([String(key), value]);
		});
		var data = { __pairs: pairs };
		// A single booking keeps its replay key as queue id; a quick batch carries
		// one key per row and gets its own id.
		var id = path === "/entries" ? (queuedField(data, "idempotency_key") || uuid()) : uuid();
		releaseSubmitLock(f);
		return put({ id: id, data: data, user: currentUser(), path: path, created: capturedAt.getTime() }).then(function () {
			rotateKeys(f);
			refreshBadge();
			toast(message || "Offline gespeichert – wird bei Verbindung gesendet.");
		}, function () {
			toast("Offline-Speicherung fehlgeschlagen — die Buchung ist NICHT gespeichert. Bitte die Eingaben nicht verwerfen und später erneut speichern.");
		});
	}

	/**
	 * Online submit through fetch with a timeout. navigator.onLine only knows
	 * whether there is a network interface; on a weak field connection it stays
	 * true while every request dies, and a native POST then ends on the browser's
	 * error page with the booking lost. The retry keys make a queued copy of a POST
	 * that did reach the server harmless: the replay deduplicates it.
	 */
	function sendOnline(f, path) {
		if (path === "/entries/quick") stampKeys();
		var body = new URLSearchParams();
		new FormData(f).forEach(function (v, key) { body.append(key, String(v)); });
		return timedFetch(path, {
			method: "POST", credentials: "same-origin", redirect: "manual",
			headers: { "Content-Type": "application/x-www-form-urlencoded" },
			body: body.toString()
		}).then(function (r) {
			// Every outcome of a booking form is a 303 to the account page, whose
			// flash cookie the redirect has already set.
			if (r.type === "opaqueredirect") { window.location.assign(accountURL(f)); return; }
			if (r.status === 502 || r.status === 503 || r.status === 504) throw new Error("gateway " + r.status);
			// Anything else is a page of its own (an error page). Re-send natively so
			// the browser shows exactly what the classic submit showed; the retry keys
			// make the second POST a no-op for whatever the first one stored.
			HTMLFormElement.prototype.submit.call(f);
		}).catch(function () {
			return capture(f, path, "Keine stabile Verbindung – offline gespeichert, wird bei Verbindung gesendet.");
		});
	}

	// Hook the booking form so an offline submit is queued instead of failing.
	var form = document.querySelector("[data-entry-form]");
	// Editing an existing group is never replayed as a new offline booking.
	if (form && form.hasAttribute("data-entry-edit")) form = null;
	if (form && !form.querySelector('[name="neighbor_id"]')) form = null;
	if (form) {
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
		// failure path would queue this booking a SECOND time under a fresh key.
		form.addEventListener("submit", function (e) {
			if (navigator.onLine) return;
			e.preventDefault();
			e.stopImmediatePropagation();
			capture(form, "/entries");
		}, true);
		// entry-form.js queues through this when its plausibility precheck cannot
		// reach the server, instead of attempting a POST that would fail the same way.
		form.treckrrQueue = function () { return capture(form, "/entries", "Keine stabile Verbindung – offline gespeichert, wird bei Verbindung gesendet."); };
	}

	// ---- Schnellerfassung offline (Ausbaukarte 100) ------------------------
	// The idempotency infrastructure carried exactly one form. The quick-entry
	// table posts to /entries/quick and becomes N bookings, so each row gets its
	// OWN key — one key for the whole submit would let a replay create the first
	// booking and silently drop the rest.
	var quick = document.querySelector("[data-quick-form]");
	function stampKeys() {
		if (!quick) return;
		quick.querySelectorAll("[data-quick-key]").forEach(function (f) {
			if (!f.value) f.value = uuid();
		});
	}
	if (quick) {
		stampKeys();
		quick.addEventListener("submit", function (e) {
			if (navigator.onLine) return;
			e.preventDefault();
			e.stopImmediatePropagation();
			capture(quick, "/entries/quick");
		}, true);
	}

	// The LAST word on a booking submit: listeners at the form (app.js's lock,
	// entry-form.js's precheck, the offline capture above) have all run by the time
	// the event bubbles here. Anything still not prevented is the real POST, and it
	// goes through fetch so a dead connection queues instead of losing the booking.
	if (window.fetch && window.URLSearchParams && window.FormData) {
		document.addEventListener("submit", function (e) {
			var f = e.target;
			if (e.defaultPrevented || !f || (f !== form && f !== quick)) return;
			e.preventDefault();
			sendOnline(f, f === quick ? "/entries/quick" : "/entries");
		});
	}

	// bfcache: a Back-restored page still carries the keys of the booking it just
	// saved. Reusing them would make the next, different booking a "duplicate".
	window.addEventListener("pageshow", function (e) {
		if (!e.persisted) return;
		if (form) { rotateKeys(form); form.dataset.checked = ""; form.dataset.checking = ""; }
		if (quick) rotateKeys(quick);
	});

	// ---- Warteschlange sichtbar machen (Ausbaukarte 72) --------------------
	// The badge was a dead counter: a booking that kept failing to send could
	// sit there indefinitely with no way to look at it, fix it or drop it.

	var panel = document.querySelector("[data-offline-panel]");
	var list = document.querySelector("[data-offline-list]");
	var badgeEl = document.querySelector("[data-offline-badge]");
	var panelLastFocus = null;
	/** Reads a scalar field without changing either historical queue format. */
	function queuedField(data, name) {
		if (!data.__pairs) return data[name] || "";
		var pair = data.__pairs.find(function (p) { return p[0] === name; });
		return pair ? pair[1] : "";
	}
	/** Identifies a batch by its endpoint, not by the shared lossless data format. */
	function isQuickBatch(item) { return item.path === "/entries/quick"; }
	/** A validation rejection the user can fix by editing the captured data. */
	function correctable(item) {
		var r = item.rejection;
		return !!r && !r.stored && (r.status === 422 || r.status === 400);
	}
	/** Server status of each quick row, keyed by row index (replay key order). */
	function quickRowStates(item, pairs) {
		var rows = (item.rejection && item.rejection.rows) || {};
		return pairs.filter(function (p) { return p[0] === "q_key"; }).map(function (p) {
			var state = Object.prototype.hasOwnProperty.call(rows, p[1]) ? rows[p[1]] : null;
			return state && typeof state === "object" ? state : null;
		});
	}
	var editableFields = {
		entry_date: "Datum", task_label: "Tätigkeit", hours: "Stunden", unit: "Einheit",
		quantity: "Menge", unit_price: "Einzelpreis", note: "Notiz",
		gespann_id: "Gespann-ID", person_id: "Person-ID (leer = ohne Helfer)",
		booking_kind: "Leistungsart (equipment, labor, quantity, fixed)",
		booking_direction: "Richtung (out = eigene Leistung, in = Gegenleistung)",
		person_hours: "Helferstunden (leer = wie Maschinenstunden)", person_rate: "Helfer-Stundensatz",
		partner_label: "Gespann / Fahrzeug", partner_rate: "Vereinbarter Maschinensatz",
		partner_person: "Person der Gegenleistung", partner_person_rate: "Stundensatz der Gegenleistung",
		partner_person_hours: "Mannstunden der Gegenleistung (leer = wie Maschinenstunden)",
		amount: "Betrag", unit_custom: "Eigene Einheit",
		tractor_id: "Traktor-ID", load_level_id: "Belastungs-ID", machine_ids: "Maschinen-ID",
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
		var fields = [], counts = {}, states = isQuickBatch(item) ? quickRowStates(item, pairs) : [];
		pairs.forEach(function (pair, index) {
			var key = pair[0];
			if (!Object.prototype.hasOwnProperty.call(editableFields, key)) return;
			counts[key] = (counts[key] || 0) + 1;
			// A row the server already stored (or holds with other data under its
			// key) cannot change by re-sending; editing it would only look like it did.
			var state = states[counts[key] - 1], locked = !!state && (state.status === "saved" || state.status === "conflict");
			var label = document.createElement("label"), text = document.createElement("span"), input = document.createElement("input");
			label.className = "field";
			text.textContent = (isQuickBatch(item) ? "Zeile " + counts[key] + " · " : "") + editableFields[key] +
				(locked ? (state.status === "saved" ? " (gespeichert)" : " (bereits gespeichert, abweichend)") : "");
			input.className = "input";
			input.type = "text";
			input.value = pair[1];
			if (locked) { input.readOnly = true; input.setAttribute("aria-readonly", "true"); }
			label.appendChild(text);
			label.appendChild(input);
			details.appendChild(label);
			if (!locked) fields.push({ key: key, index: index, input: input });
		});
		var save = document.createElement("button");
		save.type = "button";
		save.className = "btn btn--ghost btn--sm";
		save.textContent = "Korrektur speichern";
		save.addEventListener("click", function () {
			save.disabled = true;
			// Wait for any sender, in this tab or another, before touching the item.
			Promise.resolve(flushing).then(function () {
				return withQueueLock({}, function () { return correctRejected(item, fields); });
			}).then(function (saved) {
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

	/** Per-row outcome of a partially saved quick batch, in table order. */
	function quickRowSummary(item, pairs) {
		var states = quickRowStates(item, pairs), labels = { saved: "gespeichert", invalid: "abgelehnt", conflict: "bereits gespeichert (abweichend)" };
		var ul = document.createElement("ul");
		ul.className = "small";
		states.forEach(function (state, i) {
			if (!state || !labels[state.status]) return;
			var li = document.createElement("li");
			li.textContent = "Zeile " + (i + 1) + ": " + labels[state.status] + (state.message ? " – " + state.message : "");
			ul.appendChild(li);
		});
		return ul.children.length ? ul : null;
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
		if (isQuickBatch(item)) {
			// A quick-entry submit: several rows in one item, so it is described
			// by how many rows it carries rather than by one booking's fields.
			var rows = d.__pairs.filter(function (p) {
				return p[0] === "q_hours" && String(p[1]).trim() !== "";
			}).length;
			title.textContent = "Schnellerfassung · " + rows + " Zeile(n)";
			var firstDate = d.__pairs.find(function (p) { return p[0] === "q_date"; });
			sub.textContent = firstDate ? firstDate[1] : "ohne Datum";
		} else {
			var value = function (name) { return queuedField(d, name); };
			var kind = value("booking_kind"), unit = value("unit");
			var kindLabels = { equipment: "Maschinen / Gespann", labor: "Mannstunden", quantity: "Mengenleistung", fixed: "Freie Position" };
			var qty = kind === "fixed" ? (value("amount") || "?") + " €" :
				unit && unit !== "h" ? (value("quantity") || "?") + " " + (unit === "__custom" ? value("unit_custom") : unit) :
				(value("hours") || "?") + " h";
			title.textContent = (value("task_label") || kindLabels[kind] || "Buchung") + " · " + qty;
			var direction = value("booking_direction");
			var directionLabel = direction === "in" ? "Gegenleistung · Ich schulde" : direction === "out" ? "Eigene Leistung · Nachbar schuldet" : "";
			sub.textContent = (value("entry_date") || "ohne Datum") + (directionLabel ? " · " + directionLabel : "") +
				(value("note") ? " · " + value("note") : "");
		}
		meta.appendChild(title);
		meta.appendChild(sub);
		var fields = d.__pairs || Object.keys(d).map(function (key) { return [key, d[key]]; });
		if (item.rejection) {
			var error = document.createElement("p");
			error.className = "small";
			if (item.rejection.stored) {
				error.textContent = "Bereits gespeichert (abweichend): " +
					(item.rejection.message || "Unter diesem Buchungsschlüssel ist schon eine Buchung gespeichert.") +
					" Bitte die gespeicherte Buchung im Konto prüfen und diese Offline-Kopie danach verwerfen — nicht neu erfassen.";
			} else if (correctable(item)) {
				error.textContent = "Bitte prüfen (HTTP " + item.rejection.status + "): " +
					(item.rejection.message || "Die Buchung wurde abgelehnt.") +
					" Die erfassten Daten bleiben erhalten. Unten korrigieren oder Stammdaten berichtigen, dann mit „Jetzt senden“ erneut versuchen. " +
					"Bereits gespeicherte Zeilen werden dabei nicht doppelt gebucht.";
			} else {
				error.textContent = "Wird nicht angenommen" + (item.rejection.status ? " (HTTP " + item.rejection.status + ")" : "") + ": " +
					(item.rejection.message || REDIRECT_MESSAGE) +
					" Die erfassten Daten bleiben erhalten. Nach der Klärung mit „Jetzt senden“ erneut versuchen.";
			}
			meta.appendChild(error);
			if (isQuickBatch(item)) {
				var summaryList = quickRowSummary(item, fields);
				if (summaryList) meta.appendChild(summaryList);
			}
			if (item.rejection.stored && queuedField(d, "neighbor_id")) {
				var account = document.createElement("a");
				account.className = "link small";
				account.href = "/neighbors/" + encodeURIComponent(queuedField(d, "neighbor_id")) +
					(queuedField(d, "year_id") ? "?year=" + encodeURIComponent(queuedField(d, "year_id")) : "");
				account.textContent = "Gespeicherte Buchungen im Konto ansehen ›";
				meta.appendChild(account);
			}
		}
		var details = document.createElement("details");
		var summary = document.createElement("summary");
		summary.textContent = "Erfasste Daten anzeigen";
		var values = document.createElement("pre");
		values.className = "offlineq__data small";
		values.textContent = fields.map(function (p) { return p[0] + ": " + p[1]; }).join("\n");
		details.appendChild(summary);
		details.appendChild(values);
		meta.appendChild(details);
		if (correctable(item)) meta.appendChild(correctionEditor(item, fields));
		var actions = document.createElement("div");
		actions.className = "btnrow";
		var backup = document.createElement("button");
		backup.type = "button";
		backup.className = "btn btn--ghost btn--sm";
		backup.textContent = "Daten sichern";
		backup.addEventListener("click", function () {
			// An explicit local backup also preserves work if its helper no longer
			// exists. Exporting never removes the queue item or changes replay keys.
			download(item, "treckrr-offline-" + String(item.id).replace(/[^a-zA-Z0-9-]/g, "_") + ".json");
		});
		var drop = document.createElement("button");
		drop.type = "button";
		drop.className = "btn btn--danger btn--sm";
		drop.textContent = "Verwerfen";
		drop.setAttribute("data-offline-drop", "");
		drop.disabled = !!flushing;
		drop.addEventListener("click", function () {
			var busy = "Die Warteschlange wird gerade gesendet — bitte kurz warten. Diese Buchung ist möglicherweise schon gespeichert.";
			if (flushing) { toast(busy); return; }
			if (!window.confirm("Diese offline erfasste Buchung verwerfen? Sie wird nicht gesendet.")) return;
			// Another tab may be sending it right now: discard only while no sender runs.
			withQueueLock({ ifAvailable: true }, function (lock) {
				if (!lock || flushing) { toast(busy); return; }
				return del(item.id);
			}).then(function () { renderQueue(); refreshBadge(); });
		});
		wrap.appendChild(meta);
		actions.appendChild(backup);
		actions.appendChild(drop);
		wrap.appendChild(actions);
		return wrap;
	}

	/** Saves JSON through a temporary link; the object URL outlives the click briefly. */
	function download(data, name) {
		var url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
		var a = document.createElement("a");
		a.href = url;
		a.download = name;
		document.body.appendChild(a);
		a.click();
		a.remove();
		setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
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

	/** Loads the current user's queue before revealing the sheet and moving focus to its close control. */
	function openPanel() {
		if (!panel) return;
		panelLastFocus = document.activeElement;
		renderQueue().then(function () {
			panel.hidden = false;
			var close = panel.querySelector("[data-offline-close]");
			if (close) close.focus();
		});
	}
	/** Hides the queue sheet and restores its opener's focus without discarding or replaying queued bookings. */
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
		/** Handles Escape and, when the shared guard is available, wraps focus only while the sheet is visible. */
		document.addEventListener("keydown", function (e) {
			if (panel.hidden) return;
			if (e.key === "Escape") { e.preventDefault(); closePanel(); }
			else if (window.TreckrrDialog) window.TreckrrDialog.trapFocus(panel, e);
		});
	}

	// ---- Abmelden mit ungesendeten Buchungen --------------------------------
	// The queue holds neighbors, amounts, rates, person names and notes. Logging
	// out used to leave all of it on a possibly shared device without a word. The
	// server cannot see IndexedDB, so the decision is taken here, before the POST.
	function askLogout(n) {
		var intro = (n === 1 ? "1 offline erfasste Buchung ist" : n + " offline erfasste Buchungen sind") +
			" noch nicht gesendet. Sie enthalten Kunden- und Abrechnungsdaten und bleiben sonst auf diesem Gerät gespeichert.";
		if (typeof HTMLDialogElement !== "function") {
			return Promise.resolve(window.confirm(intro + "\n\nOK: behalten und abmelden – sie werden nach Ihrer nächsten Anmeldung auf diesem Gerät gesendet.\nAbbrechen: angemeldet bleiben.") ? "keep" : "cancel");
		}
		return new Promise(function (resolve) {
			var dialog = document.createElement("dialog"), card = document.createElement("form");
			dialog.className = "modal";
			dialog.setAttribute("aria-labelledby", "offline-logout-msg");
			card.method = "dialog";
			card.className = "modal__card";
			var msg = document.createElement("p"), hint = document.createElement("p"), actions = document.createElement("div");
			msg.className = "modal__msg";
			msg.id = "offline-logout-msg";
			msg.textContent = intro;
			hint.className = "muted small";
			hint.textContent = "„Behalten“ sendet sie nach Ihrer nächsten Anmeldung auf diesem Gerät. „Sichern und löschen“ lädt sie als Datei herunter und entfernt sie von diesem Gerät.";
			actions.className = "modal__actions";
			// Three actions do not fit one row of the narrow card; wrap instead of
			// pushing the first one out of the dialog. (CSSOM, allowed by the CSP.)
			actions.style.flexWrap = "wrap";
			[["cancel", "btn btn--ghost", "Abbrechen"], ["export", "btn btn--ghost", "Sichern und löschen"],
				["keep", "btn btn--primary", "Behalten und abmelden"]].forEach(function (b) {
				var button = document.createElement("button");
				button.type = "submit";
				button.value = b[0];
				button.className = b[1];
				button.textContent = b[2];
				actions.appendChild(button);
			});
			card.appendChild(msg);
			card.appendChild(hint);
			card.appendChild(actions);
			dialog.appendChild(card);
			dialog.addEventListener("close", function () {
				resolve(dialog.returnValue || "cancel");
				dialog.remove();
			});
			document.body.appendChild(dialog);
			dialog.showModal();
			actions.firstChild.focus();
		});
	}
	function exportAndDiscard(items) {
		download({ exported: new Date().toISOString(), items: items }, "treckrr-offline-" + localDate(new Date()) + ".json");
		// Wait for a running sender: an item it is posting must not vanish under it.
		return Promise.resolve(flushing).then(function () {
			return withQueueLock({}, function () {
				return Promise.all(items.map(function (item) { return del(item.id); }));
			});
		});
	}
	document.querySelectorAll('form[action="/logout"]').forEach(function (f) {
		f.addEventListener("submit", function (e) {
			if (f.dataset.queueChecked === "1") return;
			e.preventDefault();
			var logout = function () {
				f.dataset.queueChecked = "1";
				HTMLFormElement.prototype.submit.call(f);
			};
			all().then(function (items) {
				var mine = items.filter(ownedByCurrentUser);
				if (!mine.length) return logout();
				return askLogout(mine.length).then(function (choice) {
					if (choice === "keep") return logout();
					if (choice === "export") {
						return exportAndDiscard(mine).then(function () {
							// Give the download a moment to start before the page unloads.
							setTimeout(logout, 400);
						});
					}
					releaseSubmitLock(f);
					openPanel();
				});
			}).catch(logout); // A broken local store must never block logging out.
		}, true);
	});

	window.addEventListener("online", function () { flush(); });
	maintain().then(function () {
		refreshBadge();
		flush();
	});
})();
