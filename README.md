# fullstack-bundler

Frontend builds for [fullstack](https://github.com/shibisty/fullstack.go) without Node.js: TypeScript,
JSX and CSS are bundled by [esbuild](https://esbuild.github.io) running inside your Go program.
No `node_modules` toolchain, no separate watcher process.

## Installation

```json
{
    "dependencies": {
        "fullstack-bundler": "github:shibisty/fullstack.go-bundler"
    }
}
```

```go
import "fullstack-bundler" // package bundler
```

## In an application

```go
//go:embed all:public/build
var build embed.FS

app.Register(&bundler.Provider{
    Options: bundler.Options{
        Entries: []string{"resources/js/app.tsx", "resources/css/app.css"},
    },
    Build: bundler.Sub(build, "public/build"), // the production build, inside the binary
})
```

Give the views the template functions and write the tags in the layout:

```go
assets := fullstack.MustMake[*bundler.Assets](app)
v, err := view.New(views, view.Options{Dir: "views", Funcs: assets.Funcs()})
```

```html
<head>
  {{assets "resources/css/app.css" "resources/js/app.tsx"}}
</head>
```

which writes `<link rel="stylesheet" href="/build/css/app-X7Q1A2BC.css">`, the CSS the script imports
and `<script type="module" src="/build/js/app-4F2KQ9ZL.js"></script>`. `{{asset "..."}}` is one URL.

Commit an empty `public/build/.gitkeep` (and ignore the rest of `public/build/`): `//go:embed` needs
the directory to exist in a fresh checkout, and builds keep that file.

| | |
|---|---|
| `APP_ENV=local` | the app builds its frontend at start and rebuilds on every save (`ASSETS_WATCH=false` turns it off); files are not cached |
| production | `go run . assets:build` (before `go build`, so that `embed` picks it up); the app serves `Build`, or `public/build` from disk without it; hashed files are cached for a year |
| `APP_ENV=testing` | without a build, `{{assets}}` writes nothing, so HTTP tests need no frontend build |

Commands: `go run . assets:build`, `go run . assets:watch`.

## Live reload

While the app watches (`APP_ENV=local`), `{{assets}}` also adds a small script, and the browser
follows the code:

| What changed | The page |
|---|---|
| CSS (also CSS imported from JS) | swaps the stylesheets in place, without a reload, keeping state and scroll |
| TypeScript or JSX | reloads |
| a broken build | renders anyway and shows the errors, with file and line, over itself; the next good build reloads it |
| Go code, templates, `.env` (`gtr dev` rebuilds and restarts the app) | reloads once the new process is up |

With `gtr dev` this is the whole development loop: one command, one process, no Node.js. The
script talks to the app over Server-Sent Events at `/build/__live`; `ASSETS_LIVE=false` turns it
off and keeps rebuilding. Pages that do not use `{{assets}}` get no live reload.

## Islands

Pages stay HTML rendered by Go templates; the interactive parts are *islands*: components in
`resources/islands/`, one per file, each with a default export.

```tsx
// resources/islands/Counter.tsx
import { useState } from "preact/hooks";

export default function Counter(props: { start: number }) {
  const [n, setN] = useState(props.start);
  return <button onClick={() => setN(n + 1)}>{n}</button>;
}
```

```html
{{island "Counter" (dict "start" 3)}}
{{island "charts/Sales" .Stats "visible"}}           <!-- resources/islands/charts/Sales.tsx -->
{{island "Search" nil "idle"}}
```

`{{island}}` writes `<fullstack-island data-island="Counter" data-props="{&#34;start&#34;:3}">`;
the props are any Go value, sent as JSON. `{{assets}}` adds the islands loader (about 1 KB) when
the folder has islands; it mounts each island of the page, loading only the components the page
uses, each a separate chunk with Preact:

| | mounted |
|---|---|
| (default, `"load"`) | at once |
| `"idle"` | when the browser is idle |
| `"visible"` | when scrolled near the viewport |

A `template.HTML` argument (or text) is shown until the island mounts. Files and folders starting
with `_` are helpers, not islands; so are `*.test.*` and `.d.ts`. A name the build does not know
is a template error (not in tests). Islands inserted later (HTML fetched by a script) mount with
`window.fullstackIslands.mount(element)`. Install the runtime with `gtr add npm:preact`;
`JSXImportSource: "react"` mounts with `react-dom/client` instead.

Islands are mounted, not hydrated: Go cannot render Preact components, so the server sends the
element and the props, and what is inside the element before mounting is a placeholder.

## Navigation without reloads

With `Navigate: true`, `{{assets}}` adds a script (about 3 KB gzipped) that makes the site behave like
a single-page app while the server keeps rendering HTML:

- a link to the site, or a form sent to it, loads the next page with `fetch` and swaps in its
  `<body>`, `<title>` and `<head>` tags; scripts, styles and the islands' runtime stay loaded;
- forms keep working as they are: a redirect after POST lands on the new page, a 422 with errors
  replaces the form page;
- Back and Forward show the page from a short cache with its scroll position, then refresh it;
- a link hovered for 65 ms is fetched in advance, so the click is instant;
- a thin progress bar shows after 150 ms;
- islands are unmounted before a swap and mounted after it;
- when the scripts or styles of the next page differ (a deploy), the page loads the ordinary way,
  as on any error and for anything that is not HTML (JSON, files).

Requests carry `X-Fullstack-Navigate: 1` (prefetches also `X-Fullstack-Prefetch: 1`). Page code
that runs on load listens for `fullstack:load`, which fires after the first load and after every
swap; `fullstack:visit` fires before a visit and can be cancelled:

```ts
document.addEventListener("fullstack:load", () => {
  document.querySelectorAll("[data-chart]").forEach(drawChart);
});
```

When the app's view engine sends its templates (`view.Options.ClientFuncs`, see fullstack's README),
the script fetches the template pack once the first page is idle and from then on asks for pages as
data: the server sends the page's data (no layout, no markup) and the page is rendered in the
browser, with html/template's escaping. A page the browser cannot render comes as HTML, as before;
a new pack version (a deploy, an edited template) is picked up on the next response.

`data-reload` on a link or form (or an element around it) loads the ordinary way; so do links with
`target` or `download`, other origins and Ctrl/Cmd-clicks. `data-prefetch="false"` stops prefetching
a link. `<html data-view-transitions>` animates swaps with the View Transitions API where the
browser has it. `window.fullstackNavigate.visit("/notes")` visits from code.

## Installable and offline (PWA)

```go
bundler.Options{
    Entries:  []string{"resources/js/app.ts", "resources/css/app.css", "resources/icon.svg"},
    Navigate: true,
    PWA: &bundler.PWA{
        ThemeColor: "#3b82f6",
        Icons:      []bundler.Icon{{Src: "resources/icon.svg", Sizes: "any", Type: "image/svg+xml"}},
        Offline:    "/offline", // a page of the app shown offline for pages never seen
    },
}
```

The app then serves `/manifest.webmanifest` (name from `APP_NAME`, icons given as entries become their
built files) and `/sw.js`, a service worker that `{{assets}}` registers:

- on the first visit it caches the whole build (`Manifest.Files`: scripts, styles, chunks, islands,
  images) and the template pack; from then on files come from the cache, and a deploy installs a new
  worker that drops the old build;
- pages and their data come from the network first; the last answer for each URL is kept, so pages
  seen before open offline, swapped in by the navigation as usual; a page never seen shows `Offline`;
- a response with `Cache-Control: no-store` or `private` is never kept: send it for pages that must not
  stay on a shared computer. On logout, `Clear-Site-Data: "cache", "storage"` (the example does it) or
  `window.fullstackPWA.clear()` forgets what was kept.

While developing (`APP_ENV=local`) nothing is registered, and `/sw.js` is a worker that removes itself,
so no cache stands between you and the code. To try the PWA locally, run a production build:
`go run . assets:build`, then `APP_ENV=production go run .`.

## Options

| Option | Default | |
|---|---|---|
| `Entries` | — | `.ts`, `.tsx`, `.js`, `.jsx`, `.css` files |
| `OutDir` | `public/build` | emptied before every production build |
| `PublicPath` | `/build/` | where OutDir is served |
| `JSXImportSource` | `preact` | the JSX runtime (`react` works the same) |
| `Target` | `es2020` | |
| `Define`, `External`, `NodePaths` | | as in esbuild; `process.env.NODE_ENV` is defined |
| `Dev` | `false` | no minification, no hashes, inline source maps |
| `Islands` | `resources/islands` | the island components; `"-"` turns islands off |
| `Navigate` | `false` | swap pages in place on links and forms (above) |
| `PWA` | `nil` | web app manifest and service worker (above) |

Bundles are ES modules with code splitting: `import("./page")` becomes a separate chunk. Images and
fonts imported from CSS or JS become hashed files under `assets/`.

## Without fullstack

```go
m, err := bundler.Build(ctx, bundler.Options{Entries: []string{"web/app.ts"}})
tags, err := m.Tags("web/app.ts")

err = bundler.Watch(ctx, opts, func(m *bundler.Manifest, err error) { ... })
```

Build errors are a `*bundler.BuildError` listing every problem as `file:line:column: message`.

## License

MIT
