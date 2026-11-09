// fullstack-bundler service worker (Options.PWA). The server puts the build's
// settings in front of this file: self.__FULLSTACK = {version, precache,
// publicPath, pack, offline}.
//
//   - the build (hashed files) and the template pack are cached when the
//     worker installs, after the first visit; files are served from the
//     cache from then on, and the cache of an older build is dropped;
//   - pages (HTML) and their data come from the network first; the last
//     answer of each URL is kept, so pages seen before open offline; a
//     response with Cache-Control no-store or private is never kept;
//   - offline, a page never seen shows the offline page (PWA.Offline);
//   - postMessage("clear") forgets the kept pages and data; so does a
//     response with Clear-Site-Data: "storage" (on logout), in the browser.
const C = self.__FULLSTACK;
const STATIC = "fullstack-static-" + C.version;
const PAGES = "fullstack-pages";
const DATA = "fullstack-data";
const PAGE_JSON = "application/vnd.fullstack.page+json";

self.addEventListener("install", (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(STATIC);
    // one by one: a file that fails does not stop the others
    await Promise.all(C.precache.map((url) => cache.add(new Request(url, { credentials: "same-origin" })).catch(() => {})));
    await self.skipWaiting();
  })());
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    for (const name of await caches.keys()) {
      if (name.startsWith("fullstack-static-") && name !== STATIC) await caches.delete(name);
    }
    await self.clients.claim();
  })());
});

self.addEventListener("message", (event) => {
  if (event.data === "clear") event.waitUntil(Promise.all([caches.delete(PAGES), caches.delete(DATA)]));
});

function keepable(res) {
  return res && res.ok && res.type === "basic" && !/no-store|private/i.test(res.headers.get("Cache-Control") || "");
}

async function cacheFirst(req) {
  const hit = await caches.match(req, { cacheName: STATIC });
  if (hit) return hit;
  const res = await fetch(req);
  if (keepable(res)) (await caches.open(STATIC)).put(req, res.clone());
  return res;
}

// networkFirst: the network, else what was kept for the URL (in cacheName,
// then in the other caches given: a page kept as HTML serves a request for
// its data too), else the fallback page.
async function networkFirst(req, cacheName, fallback, also = []) {
  try {
    const res = await fetch(req);
    if (keepable(res) && !res.redirected) (await caches.open(cacheName)).put(req, res.clone());
    return res;
  } catch (err) {
    for (const name of [cacheName, ...also]) {
      const hit = await caches.match(req.url, { cacheName: name, ignoreVary: true });
      if (hit) return hit;
    }
    if (fallback) {
      const page = await caches.match(fallback, { cacheName: STATIC, ignoreVary: true });
      if (page) return page;
    }
    throw err;
  }
}

self.addEventListener("fetch", (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== location.origin) return;
  if (url.pathname.startsWith(C.publicPath)) {
    event.respondWith(cacheFirst(req));
  } else if (url.pathname === C.pack) {
    event.respondWith(networkFirst(req, STATIC));
  } else if (req.mode === "navigate") {
    event.respondWith(networkFirst(req, PAGES, C.offline));
  } else if ((req.headers.get("Accept") || "").includes(PAGE_JSON)) {
    event.respondWith(networkFirst(req, DATA, null, [PAGES]));
  } else if ((req.headers.get("X-Fullstack-Navigate") || "") === "1") {
    event.respondWith(networkFirst(req, PAGES, null, [DATA]));
  }
});
