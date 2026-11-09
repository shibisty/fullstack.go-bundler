package bundler

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// Islands: the page is HTML rendered by Go templates; interactive parts are
// components in Options.Islands (default resources/islands), one per file,
// mounted in the browser where a template puts {{island "Counter" .Props}}.
//
// The build adds an entry, IslandsEntry, with a small loader: it finds the
// <fullstack-island> elements of the page and imports only the components
// they name, each a separate chunk with the JSX runtime.

// IslandsEntry is the manifest key of the islands loader.
const IslandsEntry = "@islands"

const (
	islandsNS = "fullstack-islands" // the loader
	islandNS  = "fullstack-island"  // one component with its mount function
)

var islandName = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)

var islandExts = map[string]bool{".tsx": true, ".jsx": true, ".ts": true, ".js": true}

// findIslands lists the components in dir: name (path without extension,
// with /) → file. Files and folders starting with _ or . are helpers, not
// islands; so are tests and .d.ts files.
func findIslands(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		base := e.Name()
		if p != dir && (strings.HasPrefix(base, "_") || strings.HasPrefix(base, ".")) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(base)
		if e.IsDir() || !islandExts[ext] || strings.HasSuffix(base, ".d.ts") ||
			strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		name := strings.TrimSuffix(filepath.ToSlash(rel), ext)
		if !islandName.MatchString(name) {
			return fmt.Errorf("bundler: island %q: names may hold letters, digits, - and _", filepath.ToSlash(rel))
		}
		if prev, ok := out[name]; ok {
			return fmt.Errorf("bundler: island %q is both %s and %s", name, filepath.Base(prev), base)
		}
		out[name] = p
		return nil
	})
	return out, err
}

// islandsDir is the absolute islands folder, or "" when it does not exist.
func (o Options) islandsDir() string {
	if o.Islands == "-" {
		return ""
	}
	dir := filepath.Join(o.Root, filepath.FromSlash(o.Islands))
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ""
	}
	return dir
}

// islandsPlugin serves the loader and the per-island modules. The folder is
// listed on every build, and watched, so a new island needs no restart.
func (o Options) islandsPlugin(dir string) api.Plugin {
	return api.Plugin{Name: "fullstack-islands", Setup: func(pb api.PluginBuild) {
		pb.OnResolve(api.OnResolveOptions{Filter: `^fullstack-islands?(:|$)`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if name, ok := strings.CutPrefix(a.Path, islandNS+":"); ok {
				return api.OnResolveResult{Path: name, Namespace: islandNS}, nil
			}
			return api.OnResolveResult{Path: islandsNS, Namespace: islandsNS}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: islandsNS}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
			islands, err := findIslands(dir)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			src := islandsLoader(islands)
			return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS, ResolveDir: o.Root, WatchDirs: watchDirs(dir)}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: islandNS}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			islands, err := findIslands(dir)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			file, ok := islands[a.Path]
			if !ok {
				return api.OnLoadResult{}, fmt.Errorf("no island %q in %s", a.Path, o.Islands)
			}
			src := o.islandModule(file)
			return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS, ResolveDir: o.Root}, nil
		})
	}}
}

func watchDirs(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// islandModule imports a component and exports the function that mounts
// it, which returns the function that unmounts it.
func (o Options) islandModule(file string) string {
	f, _ := json.Marshal(file)
	if o.JSXImportSource == "react" {
		return `import C from ` + string(f) + `;
import { createElement } from "react";
import { createRoot } from "react-dom/client";
export default function mount(el, props) {
  el.replaceChildren();
  const root = createRoot(el);
  root.render(createElement(C, props));
  return () => root.unmount();
}
`
	}
	return `import C from ` + string(f) + `;
import { h, render } from ` + mustJSON(o.JSXImportSource) + `;
export default function mount(el, props) {
  el.replaceChildren();
  render(h(C, props), el);
  return () => render(null, el);
}
`
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// islandsLoader mounts the islands of the page: at once, when idle, or when
// scrolled into view; window.fullstackIslands.mount(root) mounts islands
// inserted later (fetched HTML) and unmount(root) releases them before the
// HTML is replaced (page navigation does both).
func islandsLoader(islands map[string]string) string {
	names := make([]string, 0, len(islands))
	for n := range islands {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("const islands = {\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %s: () => import(%s),\n", mustJSON(n), mustJSON(islandNS+":"+n))
	}
	b.WriteString(`};
const seen = new WeakSet();
const mounted = new Map(); // element → unmount
const io = typeof IntersectionObserver === "function"
  ? new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        io.unobserve(e.target);
        const el = e.target.closest("fullstack-island");
        if (el && el.isConnected) load(el);
      }
    }, { rootMargin: "200px" })
  : null;
const idle = window.requestIdleCallback || ((fn) => setTimeout(fn, 200));

async function load(el) {
  const name = el.dataset.island;
  const importer = islands[name];
  if (!importer) {
    console.error("fullstack: no island " + JSON.stringify(name) + " (resources/islands)");
    return;
  }
  try {
    const props = el.dataset.props ? JSON.parse(el.dataset.props) : {};
    const mod = await importer();
    if (!el.isConnected) return; // the page changed meanwhile
    const unmount = mod.default(el, props);
    if (typeof unmount === "function") mounted.set(el, unmount);
  } catch (err) {
    console.error("fullstack: island " + JSON.stringify(name) + " failed", err);
  }
}

// box is what IntersectionObserver can see: the island element has no box
// of its own (display: contents), so its first element, or a placeholder.
function box(el) {
  if (el.firstElementChild) return el.firstElementChild;
  const span = document.createElement("span");
  el.append(span);
  return span;
}

function mount(root = document) {
  for (const el of root.querySelectorAll("fullstack-island")) {
    if (seen.has(el)) continue;
    seen.add(el);
    const when = el.dataset.hydrate;
    if (when === "visible" && io) io.observe(box(el));
    else if (when === "idle") idle(() => load(el));
    else load(el);
  }
}

function unmount(root = document) {
  for (const [el, fn] of mounted) {
    if (root === document || root.contains(el)) {
      mounted.delete(el);
      try { fn(); } catch (err) { console.error(err); }
    }
  }
}

window.fullstackIslands = { mount, unmount };
mount();
`)
	return b.String()
}

// islandHydrate are the moments an island can be mounted.
var islandHydrate = map[string]bool{"load": true, "idle": true, "visible": true}

// Island writes the element an island mounts into:
//
//	{{island "Counter"}}
//	{{island "Counter" (dict "start" 3)}}        props, as JSON
//	{{island "Chart" .Stats "visible"}}          mounted when scrolled into view ("idle", "load")
//	{{island "Search" nil "<p>Loading…</p>"}}    shown until it mounts (template.HTML or text)
//
// known lists the islands of the build (nil: not checked).
func Island(known map[string]bool, name string, args ...any) (template.HTML, error) {
	if !islandName.MatchString(name) {
		return "", fmt.Errorf("island %q: not a valid name", name)
	}
	if known != nil && !known[name] {
		names := make([]string, 0, len(known))
		for n := range known {
			names = append(names, n)
		}
		sort.Strings(names)
		list := "none"
		if len(names) > 0 {
			list = strings.Join(names, ", ")
		}
		return "", fmt.Errorf("island %q is not in the islands folder (islands: %s)", name, list)
	}
	var props any
	var hydrate string
	var inner template.HTML
	if len(args) > 0 {
		props = args[0]
	}
	for _, a := range args[min(1, len(args)):] {
		switch v := a.(type) {
		case template.HTML:
			inner = v
		case string:
			if islandHydrate[v] {
				hydrate = v
			} else {
				inner = template.HTML(template.HTMLEscapeString(v))
			}
		default:
			return "", fmt.Errorf("island %q: unexpected argument %v (want \"load\", \"idle\", \"visible\" or the HTML shown before it mounts)", name, a)
		}
	}
	var b strings.Builder
	b.WriteString(`<fullstack-island data-island="` + template.HTMLEscapeString(name) + `"`)
	if props != nil {
		data, err := json.Marshal(props)
		if err != nil {
			return "", fmt.Errorf("island %q: props: %w", name, err)
		}
		if string(data) != "null" {
			b.WriteString(` data-props="` + template.HTMLEscapeString(string(data)) + `"`)
		}
	}
	if hydrate != "" && hydrate != "load" {
		b.WriteString(` data-hydrate="` + hydrate + `"`)
	}
	b.WriteString(` style="display:contents">` + string(inner) + `</fullstack-island>`)
	return template.HTML(b.String()), nil
}
