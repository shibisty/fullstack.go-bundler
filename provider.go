package bundler

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/evanw/esbuild/pkg/api"

	"fullstack"
	"fullstack/env"
	"fullstack/view"
)

// Assets is the application's built frontend, in the container: the
// manifest of the last build and the template functions that write tags.
type Assets struct {
	mu      sync.RWMutex
	m       *Manifest
	err     error
	testing bool
	built   chan struct{} // closed after the first dev build
	once    sync.Once
	live    *live // while watching: live reload
	dev     bool  // watching (no service worker)
	pwa     *PWA
}

func (a *Assets) set(m *Manifest, err error) {
	a.mu.Lock()
	if err == nil {
		a.m = m
	}
	a.err = err
	a.mu.Unlock()
	a.once.Do(func() { close(a.built) })
}

// Manifest returns the current manifest, or the error of the last build.
func (a *Assets) Manifest() (*Manifest, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.err != nil {
		return nil, a.err
	}
	if a.m == nil {
		return nil, errors.New("bundler: no build yet")
	}
	return a.m, nil
}

// Tags writes the tags of entries; in tests without a build it writes
// nothing, so pages render. While the provider watches, it adds the live
// reload script, and a failed build writes only that script: the page
// renders and shows the build's errors over itself.
func (a *Assets) Tags(entries ...string) (template.HTML, error) {
	m, err := a.Manifest()
	if err != nil {
		if a.testing {
			return "", nil
		}
		if a.live != nil {
			return template.HTML(a.live.tag()), nil
		}
		return "", err
	}
	tags, err := m.Tags(entries...)
	if err != nil && a.testing {
		return "", nil
	}
	for _, auto := range []string{IslandsEntry, NavigateEntry} {
		if _, ok := m.Entries[auto]; ok && err == nil && !contains(entries, auto) {
			more, _ := m.Tags(auto)
			tags += more
		}
	}
	if err == nil && a.pwa != nil {
		tags = template.HTML(a.pwa.tags()) + tags
		if _, ok := m.Entries[PWAEntry]; ok && !a.dev && !contains(entries, PWAEntry) {
			more, _ := m.Tags(PWAEntry)
			tags += more
		}
	}
	if err == nil && a.live != nil {
		tags += template.HTML(a.live.tag())
	}
	return tags, err
}

// URL returns the URL of an entry's output ({{asset "resources/js/app.tsx"}}).
func (a *Assets) URL(entry string) (string, error) {
	m, err := a.Manifest()
	if err != nil {
		if a.testing {
			return "", nil
		}
		return "", err
	}
	as, err := m.Asset(entry)
	if err != nil && a.testing {
		return "", nil
	}
	return as.File, err
}

// Island writes the element of an island component (see the package
// function Island); a name the last build does not know is an error, except
// in tests and while a broken build is being fixed.
func (a *Assets) Island(name string, args ...any) (template.HTML, error) {
	var known map[string]bool
	if m, err := a.Manifest(); err == nil && !a.testing {
		known = map[string]bool{}
		for _, n := range m.Islands {
			known[n] = true
		}
	}
	return Island(known, name, args...)
}

// Funcs are the template functions {{assets "resources/js/app.tsx"}} (the
// tags, with the islands loader when there are islands), {{asset "..."}}
// (a URL) and {{island "Counter" .Props}}, for view.Options.Funcs.
func (a *Assets) Funcs() template.FuncMap {
	return template.FuncMap{"assets": a.Tags, "asset": a.URL, "island": a.Island}
}

// ClientFuncs are the browser side of Funcs, for view.Options.ClientFuncs:
// pages using {{assets}}, {{asset}} and {{island}} can be rendered in the
// browser (Options.Navigate).
func (a *Assets) ClientFuncs() map[string]view.ClientFunc {
	return map[string]view.ClientFunc{
		"assets": {Const: true},
		"asset":  {Const: true},
		"island": {JS: "builtin:island"},
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Provider builds and serves the frontend of a fullstack application:
//
//	//go:embed public/build
//	var build embed.FS
//
//	app.Register(&bundler.Provider{
//	    Options: bundler.Options{Entries: []string{"resources/js/app.tsx", "resources/css/app.css"}},
//	    Build:   bundler.Sub(build, "public/build"),
//	})
//
// With ASSETS_WATCH on (the default with APP_ENV=local) it builds in the
// application's own process and rebuilds on every change; otherwise it
// serves the build made by `go run . assets:build`, from Build (embedded in
// the binary) or from OutDir on disk. Files are served at PublicPath, hashed
// ones cached for a year.
type Provider struct {
	Options
	// Build is the production build (OutDir) embedded in the binary; nil
	// reads OutDir from disk.
	Build fs.FS

	assets *Assets
}

// Sub is fs.Sub that panics on a bad directory name (for embed.FS in a
// Provider literal).
func Sub(fsys fs.FS, dir string) fs.FS {
	s, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return s
}

// Register adds *Assets to the container and the commands assets:build and
// assets:watch.
func (p *Provider) Register(app *fullstack.App) error {
	p.assets = &Assets{built: make(chan struct{}), testing: app.IsTesting()}
	fullstack.Set(app, p.assets)
	app.AddCommand(
		fullstack.Command{Name: "assets:build", Description: "Build the frontend for production (hashed names, minified)",
			Run: func(ctx context.Context, app *fullstack.App, _ []string) error {
				start := time.Now()
				_, err := Build(ctx, p.Options)
				if err != nil {
					return err
				}
				fmt.Fprintf(app.Stdout, "Built %d entries into %s in %s\n", len(p.Entries), p.outName(), time.Since(start).Round(time.Millisecond))
				return nil
			}},
		fullstack.Command{Name: "assets:watch", Description: "Build the frontend for development and rebuild on changes",
			Run: func(ctx context.Context, app *fullstack.App, _ []string) error {
				ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
				defer stop()
				fmt.Fprintln(app.Stdout, "Watching for changes (Ctrl+C to stop)")
				return Watch(ctx, p.Options, func(_ *Manifest, err error) {
					if err != nil {
						fmt.Fprintln(app.Stderr, err)
						return
					}
					fmt.Fprintf(app.Stdout, "%s built %d entries\n", time.Now().Format("15:04:05"), len(p.Entries))
				})
			}},
	)
	return nil
}

func (p *Provider) outName() string {
	if p.OutDir == "" {
		return "public/build"
	}
	return p.OutDir
}

func (p *Provider) publicPath() string {
	pp := p.PublicPath
	if pp == "" {
		pp = "/build/"
	}
	return strings.TrimSuffix(pp, "/")
}

// Boot starts the watcher (development) or loads the manifest, and mounts
// the file server at PublicPath.
func (p *Provider) Boot(app *fullstack.App) error {
	// Console commands (routes:list, migrate) neither watch nor need a build.
	watching := env.Bool("ASSETS_WATCH", app.IsLocal()) && !app.InConsole()
	p.assets.dev, p.assets.pwa = watching, p.PWA
	if p.PWA != nil {
		servePWA(app.Router, p.PWA, app.Config.Name, p.assets, p.publicPath()+"/", watching)
	}
	var files fs.FS
	switch {
	case watching:
		ctx, stop := context.WithCancel(context.Background())
		app.OnShutdown(func(context.Context) error { stop(); return nil })
		lv := newLive(p.dir(), p.publicPath()+"/", app.Stopping())
		if env.Bool("ASSETS_LIVE", true) {
			p.assets.live = lv
		}
		go func() {
			if err := watch(ctx, p.Options, func(res *api.BuildResult, m *Manifest, err error) {
				if err != nil {
					app.Log.Error("assets: build failed", "error", err)
				} else {
					app.Log.Debug("assets: built", "entries", len(m.Entries))
				}
				p.assets.set(m, err)
				lv.built(res, err)
			}); err != nil {
				app.Log.Error("assets: watch", "error", err)
				p.assets.set(nil, err)
			}
		}()
		select { // the first build, so that the first page has its assets
		case <-p.assets.built:
		case <-time.After(30 * time.Second):
		}
		files = os.DirFS(p.dir())
	default:
		files = p.Build
		if files == nil {
			files = os.DirFS(p.dir())
		}
		m, err := LoadManifest(files, ManifestFile)
		if err != nil && !app.IsTesting() && !app.InConsole() {
			return err
		}
		p.assets.set(m, nil)
	}
	h := fileServer(files, watching)
	if p.assets.live != nil {
		h = withLive(h, p.assets.live)
	}
	app.Router.Mount(p.publicPath(), h)
	return nil
}

// withLive serves the live reload stream and script next to the build.
func withLive(files http.Handler, lv *live) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__live", "__live":
			lv.ServeHTTP(w, r)
		case "/__live.js", "__live.js":
			lv.script(w, r)
		default:
			files.ServeHTTP(w, r)
		}
	})
}

func (p *Provider) dir() string {
	o, err := p.Options.withDefaults()
	if err != nil {
		return p.outName()
	}
	return o.outDir()
}

var hashedName = regexp.MustCompile(`-[A-Z0-9]{8}\.[a-z0-9]+(\.map)?$`)

// fileServer serves the build: hashed files cached for a year, others
// revalidated; the manifest is not served.
func fileServer(files fs.FS, dev bool) http.Handler {
	fsrv := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/") == ManifestFile || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if !dev && hashedName.MatchString(r.URL.Path) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fsrv.ServeHTTP(w, r)
	})
}
