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

	function uuid() { return crypto.randomUUID ? crypto.randomUUID() : (Date.now() + "-" + Math.random().toString(16).slice(2)); }
	function csrf() { var i = document.querySelector('input[name="csrf_token"]'); return i ? i.value : ""; }

	function badge(n) {
		var el = document.querySelector("[data-offline-badge]");
		if (!el) return;
		el.textContent = n ? (n + " offline") : "";
		el.hidden = !n;
	}
	function refreshBadge() { all().then(function (a) { badge(a.length); }); }

	function toast(msg) {
		var t = document.createElement("div");
		t.className = "toast toast--status"; t.setAttribute("role", "status");
		t.innerHTML = '<span class="toast__msg"></span>';
		t.firstChild.textContent = msg;
		document.body.appendChild(t);
		setTimeout(function () { t.remove(); }, 4000);
	}

	var flushing = false;
	function flush() {
		if (flushing || !navigator.onLine) return;
		flushing = true;
		all().then(function (items) {
			if (!items.length) return;
			var token = csrf();
			return items.reduce(function (p, item) {
				return p.then(function () {
					var body = new URLSearchParams(item.data);
					if (token) body.set("csrf_token", token);
					return fetch("/entries", {
						method: "POST", credentials: "same-origin", redirect: "manual",
						headers: { "Content-Type": "application/x-www-form-urlencoded" },
						body: body.toString()
					}).then(function (r) {
						// A 3xx redirect (success), 2xx, or opaqueredirect all mean the server
						// accepted it. A 400 is bad data that won't self-heal → drop it too so the
						// queue can't get stuck. Only a transient/5xx leaves the item to retry.
						if (r.status < 400 || r.type === "opaqueredirect" || r.status === 400) return del(item.id);
					}).catch(function () { /* network died mid-flush: keep for next time */ });
				});
			}, Promise.resolve());
		}).then(function () { refreshBadge(); }).catch(function () {}).then(function () {
			flushing = false;
		});
	}

	// Hook the booking form so an offline submit is queued instead of failing.
	var form = document.querySelector("[data-entry-form]");
	if (form && form.querySelector('[name="neighbor_id"]')) {
		if (!form.querySelector('[name="idempotency_key"]')) {
			var k = document.createElement("input");
			k.type = "hidden"; k.name = "idempotency_key"; k.value = uuid();
			form.appendChild(k);
		}
		// Capture phase so this runs BEFORE entry-form.js's precheck interceptor,
		// whose fetch would otherwise fail offline.
		form.addEventListener("submit", function (e) {
			if (navigator.onLine) return;
			e.preventDefault();
			e.stopPropagation();
			var data = {};
			new FormData(form).forEach(function (v, key) { if (key !== "csrf_token") data[String(key)] = v; });
			var id = data.idempotency_key || uuid();
			put({ id: id, data: data }).then(function () {
				var kf = form.querySelector('[name="idempotency_key"]');
				if (kf) kf.value = uuid();
				refreshBadge();
				toast("Offline gespeichert – wird bei Verbindung gesendet.");
			});
		}, true);
	}

	window.addEventListener("online", flush);
	refreshBadge();
	flush();
})();
