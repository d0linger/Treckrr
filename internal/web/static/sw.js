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
