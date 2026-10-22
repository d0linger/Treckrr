// Treckrr service worker — app-shell caching with network-first navigation.
// __CACHE_VERSION__ is replaced at serve time with the asset content hash, so a
// new build produces a new cache name and evicts the previous one on activate.
const CACHE = "treckrr-__CACHE_VERSION__";
const VERSION = "__CACHE_VERSION__";
// Pages reference scripts and styles as ?v=<asset hash>; precaching the same
// URLs is what makes the cached shell usable offline. Icons and the manifest are
// referenced without a version and stay unversioned here.
const VERSIONED = [
	"/static/css/app.css",
	"/static/js/app.js",
	"/static/js/form-ux.js",
	"/static/js/favorites.js",
	"/static/js/entry-form.js",
	"/static/js/unified-booking.js",
	"/static/js/ledger-booking.js",
	"/static/js/offline.js",
	"/static/js/capture.js",
	"/static/js/app-bg.js",
	"/static/icons/favicon.svg"
].map((path) => path + "?v=" + VERSION);
const SHELL = VERSIONED.concat([
	"/static/icons/favicon.svg",
	"/static/icons/icon-192.png",
	"/static/icons/icon-512.png",
	"/offline",
	"/manifest.webmanifest"
]);

// A response may be cached only if it is a success AND, for a versioned asset,
// really is that version: during a rolling deploy an old instance can answer a
// ?v=<new> request with its old bytes. The static server names the version it
// served in X-Treckrr-Asset-Version; a mismatch is served but never stored.
function cacheable(url, res) {
	if (!res || !res.ok) return false;
	const want = new URL(url, self.location.origin).searchParams.get("v");
	return !want || res.headers.get("X-Treckrr-Asset-Version") === want;
}

self.addEventListener("install", (event) => {
	event.waitUntil(
		caches.open(CACHE).then((cache) => Promise.all(SHELL.map((url) =>
			fetch(url, { cache: "reload", credentials: "same-origin" }).then((res) => {
				// Fail the install rather than pin a stale or broken shell; the
				// browser retries the installation on a later navigation.
				if (!cacheable(url, res)) throw new Error("precache " + url + ": " + res.status);
				return cache.put(url, res);
			})
		))).then(() => self.skipWaiting())
	);
});

self.addEventListener("activate", (event) => {
	event.waitUntil(
		caches.keys().then((keys) =>
			Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))
		).then(() => self.clients.claim())
	);
});

self.addEventListener("fetch", (event) => {
	const req = event.request;
	if (req.method === "POST" && new URL(req.url).pathname === "/share-target") {
		event.respondWith(receiveShare(req));
		return;
	}
	if (req.method !== "GET") return; // never cache mutations

	const url = new URL(req.url);
	if (url.origin !== self.location.origin) return;

	// Static assets: stale-while-revalidate. Serve from cache for speed, but
	// always refresh in the background so new builds propagate on next load.
	if (url.pathname.startsWith("/static/")) {
		event.respondWith(
			caches.match(req).then((hit) => {
				const fetching = fetch(req).then((res) => {
					if (cacheable(req.url, res)) {
						const copy = res.clone();
						caches.open(CACHE).then((c) => c.put(req, copy));
					}
					return res;
				}).catch(() => hit);
				return hit || fetching;
			})
		);
		return;
	}

	// Navigations: network-first, fall back to the cached page, then the cached
	// /offline shell, and — as a last resort if even that isn't cached yet (first
	// visit already offline) — a minimal branded inline page, so a navigation
	// offline never resolves to a raw browser error.
	if (req.mode === "navigate") {
		event.respondWith(
			fetch(req).catch(() =>
				caches.match(req).then((hit) =>
					hit || caches.match("/offline").then((off) => off || OFFLINE_FALLBACK())
				)
			)
		);
	}
});

// receiveShare handles the Web Share Target: up to ten shared images are parked
// in IndexedDB on this device and the picker page opens. Nothing reaches the
// server here; the picker uploads through the normal booking photo route.
async function receiveShare(req) {
	try {
		const form = await req.formData();
		const incoming = form.getAll("photos");
		if (!incoming.length) return Response.redirect("/share-target?share-error=empty", 303);
		if (incoming.length > 10) return Response.redirect("/share-target?share-error=too-many", 303);
		if (incoming.some((f) => !f || typeof f !== "object" || !/^(image\/jpeg|image\/png|image\/gif)$/.test(f.type))) {
			return Response.redirect("/share-target?share-error=unsupported", 303);
		}
		const files = incoming.map((f) => ({ name: f.name, type: f.type, blob: f }));
		const record = { files, at: Date.now(), id: crypto.randomUUID() };
		const pending = await new Promise((resolve, reject) => {
			const open = indexedDB.open("treckrr-share", 1);
			open.onupgradeneeded = () => open.result.createObjectStore("pending");
			open.onerror = () => reject(open.error);
			open.onsuccess = () => {
				const db = open.result;
				const tx = db.transaction("pending", "readwrite");
				const store = tx.objectStore("pending");
				let occupied = false;
				// Keep the check and write together: simultaneous shares must not
				// replace photos that have not been attached or discarded yet.
				const current = store.get("shared");
				current.onsuccess = () => {
					occupied = !!(current.result && Array.isArray(current.result.files) && current.result.files.length);
					if (!occupied) store.put(record, "shared");
				};
				tx.oncomplete = () => { db.close(); resolve(occupied); };
				tx.onerror = () => { db.close(); reject(tx.error); };
				tx.onabort = () => { db.close(); reject(tx.error); };
			};
		});
		return Response.redirect(pending ? "/share-target?share-error=pending" : "/share-target", 303);
	} catch (e) {
		return Response.redirect("/share-target?share-error=storage", 303);
	}
}

// OFFLINE_FALLBACK is the SW's own tiny branded page, used only when the cached
// /offline shell is unavailable. Kept inline so it needs no network or cache.
function OFFLINE_FALLBACK() {
	const html = '<!doctype html><html lang="de"><head><meta charset="utf-8">'
		+ '<meta name="viewport" content="width=device-width,initial-scale=1">'
		+ '<title>Offline</title><style>body{margin:0;min-height:100vh;display:grid;'
		+ 'place-items:center;font-family:system-ui,sans-serif;background:#f4f2ea;color:#1b2420}'
		+ '@media(prefers-color-scheme:dark){body{background:#11140f;color:#e9ede4}}'
		+ '.c{text-align:center;max-width:22rem;padding:2rem}h1{color:#115638;margin:.2rem 0}'
		+ '@media(prefers-color-scheme:dark){h1{color:#7fce9f}}a{color:inherit}</style></head>'
		+ '<body><div class="c"><h1>Offline</h1><p>Keine Verbindung. Sobald Sie wieder online '
		+ 'sind, geht es weiter — offline erfasste Buchungen werden dann gespeichert.</p>'
		+ '<p><a href="/">Erneut versuchen</a></p></div></body></html>';
	return new Response(html, { headers: { "Content-Type": "text/html; charset=utf-8" } });
}
