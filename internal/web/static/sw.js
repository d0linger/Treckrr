// Treckrr service worker — app-shell caching with network-first navigation.
// __CACHE_VERSION__ is replaced at serve time with the asset content hash, so a
// new build produces a new cache name and evicts the previous one on activate.
const CACHE = "treckrr-__CACHE_VERSION__";
const SHELL = [
	"/static/css/app.css",
	"/static/js/app.js",
	"/static/js/entry-form.js",
	"/static/js/offline.js",
	"/static/icons/favicon.svg",
	"/static/icons/icon-192.png",
	"/static/icons/icon-512.png",
	"/offline",
	"/manifest.webmanifest"
];

self.addEventListener("install", (event) => {
	event.waitUntil(
		caches.open(CACHE).then((cache) => cache.addAll(SHELL)).then(() => self.skipWaiting())
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
	if (req.method !== "GET") return; // never cache mutations

	const url = new URL(req.url);
	if (url.origin !== self.location.origin) return;

	// Static assets: stale-while-revalidate. Serve from cache for speed, but
	// always refresh in the background so new builds propagate on next load.
	if (url.pathname.startsWith("/static/")) {
		event.respondWith(
			caches.match(req).then((hit) => {
				const fetching = fetch(req).then((res) => {
					const copy = res.clone();
					caches.open(CACHE).then((c) => c.put(req, copy));
					return res;
				}).catch(() => hit);
				return hit || fetching;
			})
		);
		return;
	}

	// Navigations: network-first, fall back to cache, then offline page.
	if (req.mode === "navigate") {
		event.respondWith(
			fetch(req).catch(() => caches.match(req).then((hit) => hit || caches.match("/offline")))
		);
	}
});
