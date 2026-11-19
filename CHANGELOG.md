# Changelog

## Unreleased

### Changed
- Renamed from `fullstack-bundler` to `@fullstack/bundler` (gtr ADR-0012): `gtr add @fullstack/bundler`, `import "fullstack/bundler"`; published to the gtr registry. Dependencies on fullstack, passport, orm and migration packages are registry ranges instead of `github:` sources.

### Added
- `bundler.Build`: TypeScript, JSX (automatic runtime, Preact by default) and CSS bundled by
  esbuild in the Go process; code splitting, minification, linked source maps, content hashes in
  file names, images and fonts as hashed files; `Dev` builds without hashes, with inline maps.
  Errors come back together with file, line and column (`BuildError`).
- `Manifest` (`public/build/manifest.json`): entry → file and the CSS a JS entry imports;
  `Tags` writes `<link>` and `<script type="module">`; `LoadManifest` reads it from an `embed.FS`.
- `bundler.Watch`: rebuilds on every change until the context ends.
- `bundler.Provider` for fullstack: `assets:build` and `assets:watch` commands; with `ASSETS_WATCH`
  (default on with `APP_ENV=local`) the application rebuilds its frontend itself; otherwise it
  serves the build from `Build` (embedded) or disk. Files at `/build/`, hashed ones cached for a
  year. `*bundler.Assets` in the container with template functions `assets` and `asset`;
  in `APP_ENV=testing` a missing build renders no tags instead of failing.
- Islands: components in `resources/islands/` (`Options.Islands`) mounted where a template writes
  `{{island "Counter" .Props}}` (props as JSON; `"load"`, `"idle"` or `"visible"`; placeholder
  HTML). `{{assets}}` adds the loader (manifest entry `@islands`, the names in `Manifest.Islands`),
  which imports only the islands on the page, each its own chunk; new islands are picked up while
  watching. Preact by default, React with `JSXImportSource: "react"`.
- `Options.Navigate`: links and forms to the site swap the page in place (fetch, history, scroll
  restoration, a cache for Back/Forward refreshed after showing, prefetch on hover, a progress bar,
  islands unmounted and mounted, View Transitions with `data-view-transitions`); a deploy, an error
  or a response that is not HTML loads the ordinary way. Events `fullstack:load` and
  `fullstack:visit`; `data-reload` and `data-prefetch="false"` opt out. Islands' mount functions
  return their unmount; `window.fullstackIslands.unmount(root)`.
- Pages rendered in the browser: with a template pack from the app (fullstack/view), navigation
  fetches pages as data and renders them with `client/templates.js`, a port of text/template's
  evaluation and html/template's escapers checked against Go in `TestBrowserTemplatesParity`
  (runs where Node is installed); pages it cannot render come as HTML. `Assets.ClientFuncs` for
  `assets`, `asset` (computed on the server) and `island`.
- `Options.PWA`: `/manifest.webmanifest` and a service worker at `/sw.js` that precaches the build
  (`Manifest.Files`) and the template pack, serves files cache-first, pages and data network-first
  with the last answer kept for offline use, an offline page, `Cache-Control: no-store`/`private`
  never kept, `fullstackPWA.clear()`; in development a worker that removes itself. Images and fonts
  given as entries map to their copied files.
- Live reload while watching: `{{assets}}` adds a script that swaps stylesheets in place when only
  CSS changed, reloads the page for other changes and when the server restarted (`gtr dev`), and
  shows build errors over the page (a failed build renders the page with the script only).
  Server-Sent Events at `/build/__live`; `ASSETS_LIVE=false` turns it off.
- Console commands (`routes:list`, `migrate`) neither watch nor need a production build.
