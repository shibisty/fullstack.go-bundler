// @fullstack/bundler page navigation
const PREFETCH_TTL = 15000;
const CACHE_SIZE = 10;
const prefetched = new Map(); // url → {promise, time}
const snapshots = new Map(); // history key → {html, scrollX, scrollY}
let current = null; // AbortController of the visit in flight
let key = (history.state && history.state.fullstack) || newKey();
history.replaceState({ ...(history.state || {}), fullstack: key }, "");
if ("scrollRestoration" in history) history.scrollRestoration = "manual";

function newKey() {
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
}

function optedOut(el) {
  return el.closest("[data-reload]") !== null;
}

function sameOrigin(url) {
  return url.origin === location.origin;
}

// Assets: the scripts and stylesheets of a document, which must not change
// between pages that are swapped in place.
function assets(doc) {
  const out = [];
  for (const el of doc.head.querySelectorAll('script[src], link[rel="stylesheet"]')) {
    const u = new URL(el.getAttribute("src") || el.getAttribute("href"), location.href);
    out.push(u.origin + u.pathname);
  }
  return out.sort().join("|");
}

// Progress bar after a short delay, so fast visits show none.
let bar = null, barTimer = 0;
function progress(on) {
  clearTimeout(barTimer);
  if (on) {
    barTimer = setTimeout(() => {
      bar = bar || document.createElement("div");
      bar.setAttribute("style", "position:fixed;top:0;left:0;height:3px;width:0;z-index:2147483647;" +
        "background:var(--fullstack-progress,#3b82f6);transition:width 8s cubic-bezier(.1,.7,.1,1)");
      document.documentElement.append(bar);
      requestAnimationFrame(() => { bar.style.width = "90%"; });
    }, 150);
  } else if (bar) {
    bar.remove();
    bar = null;
  }
}

function fire(name, detail, cancelable = false) {
  return document.dispatchEvent(new CustomEvent("fullstack:" + name, { detail, cancelable }));
}

function remember() {
  snapshots.delete(key);
  snapshots.set(key, { html: document.documentElement.outerHTML, scrollX, scrollY });
  while (snapshots.size > CACHE_SIZE) snapshots.delete(snapshots.keys().next().value);
}

// Templates: the server's template pack (fullstack/view) lets pages come
// as data and be rendered here. It is fetched once the page is idle; until
// then, and for pages the browser cannot render, the server sends HTML.
const PACK_HEADER = "X-Fullstack-Templates";
const PAGE_JSON = "application/vnd.fullstack.page+json";
const tpl = { pack: null, engine: null, loading: null, skip: new Set() };

function loadPack() {
  if (tpl.loading) return tpl.loading;
  tpl.loading = (async () => {
    try {
      const res = await fetch("/__fullstack/templates", { credentials: "same-origin" });
      if (!res.ok) return;
      const [pack, engine] = await Promise.all([res.json(), import("fullstack-templates")]);
      tpl.pack = pack;
      tpl.engine = engine;
    } catch (err) {
      console.debug("fullstack: no template pack", err);
    } finally {
      tpl.loading = null;
    }
  })();
  return tpl.loading;
}

function request(url, init = {}, withPack = true) {
  const headers = new Headers(init.headers);
  headers.set("X-Fullstack-Navigate", "1");
  if (withPack && tpl.pack) {
    headers.set("Accept", PAGE_JSON + ", text/html;q=0.9");
    headers.set(PACK_HEADER, tpl.pack.v);
    if (tpl.skip.size) headers.set(PACK_HEADER + "-Skip", [...tpl.skip].join(","));
  } else {
    headers.set("Accept", "text/html, application/xhtml+xml");
  }
  return fetch(url, { credentials: "same-origin", redirect: "follow", ...init, headers });
}

// pageOf turns a response into HTML: as it came, or rendered from data.
// null: the data could not be rendered here (the view is skipped from now).
async function pageOf(res) {
  const type = res.headers.get("Content-Type") || "";
  const v = res.headers.get(PACK_HEADER);
  if (v && tpl.pack && v !== tpl.pack.v) { tpl.pack = null; idle(loadPack); } // a deploy or an edit
  if (type.includes(PAGE_JSON)) {
    const page = await res.json();
    try {
      return tpl.engine.renderPage(tpl.pack, page.view, page.data);
    } catch (err) {
      console.debug("fullstack: " + page.view + " is rendered by the server:", err.message || err);
      tpl.skip.add(page.view);
      return null;
    }
  }
  if (!type.includes("html")) throw new Error("not a page: " + type);
  return res.text();
}

const idle = window.requestIdleCallback || ((fn) => setTimeout(fn, 300));

function prefetch(url) {
  const hit = prefetched.get(url);
  if (hit && Date.now() - hit.time < PREFETCH_TTL) return;
  const promise = request(url, { headers: { "X-Fullstack-Prefetch": "1" } })
    .then(async (res) => ({ res, html: await pageOf(res) }))
    .then((r) => { if (r.html === null) throw new Error("render on the server"); return r; });
  promise.catch(() => prefetched.delete(url));
  prefetched.set(url, { promise, time: Date.now() });
}

async function visit(url, init, { push = true, scroll = null, quiet = false } = {}) {
  if (!quiet && !fire("visit", { url: url.href }, true)) return;
  if (current) current.abort();
  const ctl = (current = new AbortController());
  if (!quiet) progress(true);
  try {
    let res, html;
    const hit = !init && prefetched.get(url.href);
    if (hit && Date.now() - hit.time < PREFETCH_TTL) {
      try { ({ res, html } = await hit.promise); } catch { res = null; }
    }
    if (!res) {
      res = await request(url.href, { ...init, signal: ctl.signal });
      html = await pageOf(res);
      if (html === null) {
        // the browser could not render the data: GET again for HTML; a
        // form cannot be sent twice, so it is the page that answered it
        if (init && !res.redirected) { location.reload(); return; }
        res = await request(res.url || url.href, { signal: ctl.signal }, false);
        html = await pageOf(res);
      }
    }
    prefetched.clear(); // pages may have changed (a form was sent)
    if (ctl !== current) return;
    const final = new URL(res.url || url.href);
    final.hash = url.hash;
    if (quiet) scroll = { x: scrollX, y: scrollY }; // the reader may have scrolled the snapshot
    const moved = final.href !== location.href;
    render(html, final, {
      push: push && (moved || !!init),
      // a form answered at the same URL, or a page that redirected elsewhere
      // on back or forward, takes the current entry's place
      replace: (!!init && !moved) || (!push && moved),
      scroll,
    });
  } catch (err) {
    if (err.name === "AbortError") return;
    console.warn("fullstack: loading " + url.href + " the ordinary way:", err.message || err);
    if (init && init.method === "POST") location.reload();
    else location.assign(url.href);
  } finally {
    if (ctl === current) { current = null; progress(false); }
  }
}

function render(html, url, { push, scroll, replace }) {
  const doc = new DOMParser().parseFromString(html, "text/html");
  if (assets(doc) !== assets(document) || doc.querySelector("[data-reload-page]")) {
    location.assign(url.href);
    return;
  }
  if (push || replace) {
    remember();
    key = newKey();
    history[replace ? "replaceState" : "pushState"]({ fullstack: key }, "", url.href);
  }
  const swap = () => {
    if (window.fullstackIslands) window.fullstackIslands.unmount(document);
    // head: everything but scripts and styles follows the new page
    const keep = (el) => el.matches('script, link[rel="stylesheet"], style');
    for (const el of [...document.head.children]) if (!keep(el)) el.remove();
    for (const el of doc.head.children) if (!keep(el)) document.head.append(document.importNode(el, true));
    document.title = doc.title;
    for (const a of doc.documentElement.attributes) document.documentElement.setAttribute(a.name, a.value);
    const body = document.importNode(doc.body, true);
    // scripts parsed by DOMParser do not run: recreate those of the body
    for (const old of body.querySelectorAll("script")) {
      const s = document.createElement("script");
      for (const a of old.attributes) s.setAttribute(a.name, a.value);
      s.textContent = old.textContent;
      old.replaceWith(s);
    }
    document.body.replaceWith(body);
    if (scroll) scrollTo(scroll.x, scroll.y);
    else if (url.hash && document.getElementById(decodeURIComponent(url.hash.slice(1)))) {
      document.getElementById(decodeURIComponent(url.hash.slice(1))).scrollIntoView();
    } else scrollTo(0, 0);
    const focus = document.querySelector("[autofocus]");
    if (focus) focus.focus();
    if (window.fullstackIslands) window.fullstackIslands.mount(document);
    fire("load", { url: url.href });
  };
  if (document.startViewTransition && !matchMedia("(prefers-reduced-motion: reduce)").matches &&
      document.documentElement.hasAttribute("data-view-transitions")) {
    document.startViewTransition(swap);
  } else swap();
}

function linkOf(e) {
  const a = e.target instanceof Element && e.target.closest("a[href]");
  if (!a || a.target && a.target !== "_self" || a.hasAttribute("download") || optedOut(a)) return null;
  const url = new URL(a.href, location.href);
  if (!sameOrigin(url) || !/^https?:$/.test(url.protocol)) return null;
  return url;
}

document.addEventListener("click", (e) => {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const url = linkOf(e);
  if (!url) return;
  // a link to an anchor of this page scrolls, as usual
  if (url.pathname === location.pathname && url.search === location.search && url.hash) return;
  e.preventDefault();
  clearTimeout(hoverTimer);
  visit(url);
});

let hoverTimer = 0;
document.addEventListener("mouseover", (e) => {
  const url = linkOf(e);
  if (!url || url.href === location.href || e.target.closest("[data-prefetch=false]")) return;
  clearTimeout(hoverTimer);
  hoverTimer = setTimeout(() => prefetch(url.href), 65);
});
document.addEventListener("mouseout", () => clearTimeout(hoverTimer));
document.addEventListener("touchstart", (e) => {
  const url = linkOf(e);
  if (url && url.href !== location.href) prefetch(url.href);
}, { passive: true });

document.addEventListener("submit", (e) => {
  const form = e.target;
  if (e.defaultPrevented || !(form instanceof HTMLFormElement) || optedOut(form)) return;
  const submitter = e.submitter;
  if (submitter && submitter.closest("[data-reload]")) return;
  const method = ((submitter && submitter.getAttribute("formmethod")) || form.getAttribute("method") || "get").toLowerCase();
  if (method === "dialog") return;
  const target = (submitter && submitter.getAttribute("formtarget")) || form.target;
  if (target && target !== "_self") return;
  const url = new URL((submitter && submitter.getAttribute("formaction")) || form.getAttribute("action") || location.href, location.href);
  if (!sameOrigin(url)) return;
  let data;
  try {
    data = new FormData(form, submitter || undefined);
  } catch {
    data = new FormData(form); // browsers before 2022: add the submitter by hand
    if (submitter && submitter.name) data.append(submitter.name, submitter.value);
  }
  e.preventDefault();
  if (method === "get") {
    url.search = new URLSearchParams(data).toString();
    visit(url);
    return;
  }
  const enctype = ((submitter && submitter.getAttribute("formenctype")) || form.enctype || "").toLowerCase();
  const body = enctype === "multipart/form-data" ? data : new URLSearchParams(data);
  visit(url, { method: "POST", body });
});

addEventListener("popstate", (e) => {
  const to = e.state && e.state.fullstack;
  if (!to) return;
  remember();
  key = to;
  const snap = snapshots.get(to);
  if (snap) {
    // the snapshot at once, then the page as it is now, keeping the scroll
    const scroll = { x: snap.scrollX, y: snap.scrollY };
    render(snap.html, new URL(location.href), { push: false, scroll });
    visit(new URL(location.href), undefined, { push: false, scroll, quiet: true });
  } else {
    visit(new URL(location.href), undefined, { push: false });
  }
});

addEventListener("pagehide", () => remember());

window.fullstackNavigate = { visit: (href) => visit(new URL(href, location.href)) };
fire("load", { url: location.href });
idle(loadPack);
