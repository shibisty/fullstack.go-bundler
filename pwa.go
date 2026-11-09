package bundler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	_ "embed"

	"fullstack/web"

	"github.com/evanw/esbuild/pkg/api"
)

// PWA makes the application installable and usable offline (Options.PWA):
// a web app manifest at /manifest.webmanifest and a service worker at
// /sw.js that caches the build and the template pack after the first visit
// and keeps the pages and data seen, for offline use.
type PWA struct {
	Name      string // the app's name (default: APP_NAME)
	ShortName string // under the icon (default Name)
	// StartURL is opened from the home screen (default "/").
	StartURL string
	// Display: "standalone" (default), "fullscreen", "minimal-ui", "browser".
	Display         string
	ThemeColor      string // the browser's toolbar ("#3b82f6")
	BackgroundColor string // the splash screen (default "#ffffff")
	// Icons; Src may be an entry of the build ("resources/icon.svg"), which
	// is replaced by its URL.
	Icons []Icon
	// Offline is a page (a URL of the app, e.g. "/offline") cached when the
	// worker installs and shown for pages never seen while offline.
	Offline string
}

// Icon is an icon of the web app manifest.
type Icon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`             // "192x192", "any" for SVG
	Type    string `json:"type,omitempty"`    // "image/png"
	Purpose string `json:"purpose,omitempty"` // "any", "maskable"
}

// PWAEntry is the manifest key of the script that registers the worker.
const PWAEntry = "@pwa"

const pwaNS = "fullstack-pwa"

var (
	//go:embed client/sw.js
	swScript string
	//go:embed client/pwa.js
	pwaScript string
)

func pwaPlugin(root string) api.Plugin {
	return api.Plugin{Name: pwaNS, Setup: func(pb api.PluginBuild) {
		pb.OnResolve(api.OnResolveOptions{Filter: `^` + pwaNS + `$`}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: pwaNS, Namespace: pwaNS}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: pwaNS}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
			src := pwaScript
			return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS, ResolveDir: root}, nil
		})
	}}
}

// webManifest is the manifest.webmanifest of the app.
func (p *PWA) webManifest(appName string, m *Manifest) []byte {
	name := p.Name
	if name == "" {
		name = appName
	}
	short := p.ShortName
	if short == "" {
		short = name
	}
	icons := make([]Icon, len(p.Icons))
	for i, ic := range p.Icons {
		if m != nil {
			if a, ok := m.Entries[ic.Src]; ok {
				ic.Src = a.File
			}
		}
		icons[i] = ic
	}
	out := map[string]any{
		"name": name, "short_name": short, "start_url": or(p.StartURL, "/"), "scope": "/",
		"display": or(p.Display, "standalone"), "background_color": or(p.BackgroundColor, "#ffffff"),
		"icons": icons,
	}
	if p.ThemeColor != "" {
		out["theme_color"] = p.ThemeColor
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// serviceWorker is /sw.js for a build: its settings and client/sw.js. The
// version changes with the build's files, so a deploy installs a new
// worker. While developing it is a worker that removes itself, so that no
// cache stands between the developer and the code.
func (p *PWA) serviceWorker(m *Manifest, publicPath string, dev bool) []byte {
	if dev || m == nil {
		return []byte(`// development: no service worker; this one removes an earlier one
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil((async () => {
  for (const name of await caches.keys()) if (name.startsWith("fullstack-")) await caches.delete(name);
  await self.registration.unregister();
})()));
`)
	}
	precache := append([]string(nil), m.Files...)
	precache = append(precache, web.PackPath)
	if p.Offline != "" {
		precache = append(precache, p.Offline)
	}
	h := sha256.Sum256([]byte(strings.Join(precache, "\n")))
	cfg, _ := json.Marshal(map[string]any{
		"version": hex.EncodeToString(h[:8]), "precache": precache, "publicPath": publicPath,
		"pack": web.PackPath, "offline": p.Offline,
	})
	return []byte("self.__FULLSTACK = " + string(cfg) + ";\n" + swScript)
}

// pwaTags are the <head> tags of the PWA.
func (p *PWA) tags() string {
	s := `<link rel="manifest" href="/manifest.webmanifest">`
	if p.ThemeColor != "" {
		s += `<meta name="theme-color" content="` + strings.ReplaceAll(p.ThemeColor, `"`, "") + `">`
	}
	return s
}

func servePWA(app interface {
	Get(string, web.Handler, ...web.Middleware) *web.Route
}, p *PWA, name string, assets *Assets, publicPath string, dev bool) {
	app.Get("/manifest.webmanifest", func(c *web.Ctx) error {
		m, _ := assets.Manifest()
		c.W.Header().Set("Content-Type", "application/manifest+json")
		c.W.Header().Set("Cache-Control", "no-cache")
		_, err := c.W.Write(p.webManifest(name, m))
		return err
	})
	app.Get("/sw.js", func(c *web.Ctx) error {
		m, _ := assets.Manifest()
		h := c.W.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("Service-Worker-Allowed", "/")
		c.W.WriteHeader(http.StatusOK)
		_, err := c.W.Write(p.serviceWorker(m, publicPath, dev))
		return err
	})
}
