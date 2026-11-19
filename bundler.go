// Package bundler builds the frontend of a fullstack application with
// esbuild, inside the Go process: TypeScript, JSX (Preact by default) and
// CSS, without Node.js (gtr package @fullstack/bundler).
//
//	m, err := bundler.Build(ctx, bundler.Options{
//	    Entries: []string{"resources/js/app.tsx", "resources/css/app.css"},
//	})
//	// public/build/js/app-4F2KQ.js, public/build/css/app-X7Q1A.css, public/build/manifest.json
//
// Production builds get content hashes in their names (cache them forever);
// the manifest maps each entry to its files, and the templates write the
// tags with {{assets "resources/js/app.tsx"}}.
package bundler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
)

// Options configure a build.
type Options struct {
	// Root is the project directory; the other paths are relative to it
	// (default: the working directory).
	Root string
	// Entries are the entry points: .ts, .tsx, .js, .jsx and .css files.
	Entries []string
	// OutDir receives the build (default "public/build"). It is emptied
	// before a production build, so it must hold nothing else.
	OutDir string
	// PublicPath is the URL OutDir is served at (default "/build/").
	PublicPath string
	// Dev builds for development: no minification, no hashes in names,
	// inline source maps.
	Dev bool
	// JSXImportSource is the JSX runtime (default "preact"; "react" works
	// the same way once installed).
	JSXImportSource string
	// Target is the JavaScript version to compile down to (default es2020).
	Target api.Target
	// Define replaces global identifiers: {"process.env.NODE_ENV": `"production"`}.
	Define map[string]string
	// NodePaths are where bare imports ("preact") are resolved, besides
	// node_modules folders.
	NodePaths []string
	// External imports are left to the browser.
	External []string
	// Islands is the folder of island components (default
	// "resources/islands"; "-" turns islands off). Without the folder there
	// are no islands.
	Islands string
	// Navigate swaps pages in place: links and forms to the site load the
	// next page with fetch, without reloading scripts and styles (the
	// NavigateEntry script, added by Assets.Tags).
	Navigate bool
	// PWA makes the app installable and usable offline: a web app manifest
	// and a service worker (see PWA). The worker is registered by the
	// PWAEntry script, which Assets.Tags adds outside development.
	PWA *PWA
}

func (o Options) withDefaults() (Options, error) {
	if o.Root == "" {
		o.Root = "."
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return o, err
	}
	o.Root = root
	if o.OutDir == "" {
		o.OutDir = "public/build"
	}
	if o.PublicPath == "" {
		o.PublicPath = "/build/"
	}
	if !strings.HasSuffix(o.PublicPath, "/") {
		o.PublicPath += "/"
	}
	if o.JSXImportSource == "" {
		o.JSXImportSource = "preact"
	}
	if o.Target == api.DefaultTarget {
		o.Target = api.ES2020
	}
	if o.Islands == "" {
		o.Islands = "resources/islands"
	}
	if len(o.Entries) == 0 && o.islandsDir() == "" && !o.Navigate && o.PWA == nil {
		return o, errors.New("bundler: no entries")
	}
	out := filepath.Clean(filepath.Join(root, o.OutDir))
	if out == root || !strings.HasPrefix(out, root+string(filepath.Separator)) {
		return o, fmt.Errorf("bundler: OutDir %q must be a directory inside the project", o.OutDir)
	}
	return o, nil
}

func (o Options) outDir() string { return filepath.Join(o.Root, o.OutDir) }

func (o Options) esbuild() api.BuildOptions {
	names := "[dir]/[name]-[hash]"
	if o.Dev {
		names = "[dir]/[name]"
	}
	define := map[string]string{"process.env.NODE_ENV": `"production"`}
	if o.Dev {
		define["process.env.NODE_ENV"] = `"development"`
	}
	for k, v := range o.Define {
		define[k] = v
	}
	b := api.BuildOptions{
		AbsWorkingDir:       o.Root,
		EntryPointsAdvanced: o.entryPoints(),
		Outdir:              o.OutDir,
		Bundle:              true,
		Splitting:           true,
		Format:              api.FormatESModule,
		Target:              o.Target,
		Platform:            api.PlatformBrowser,
		JSX:                 api.JSXAutomatic,
		JSXImportSource:     o.JSXImportSource,
		Define:              define,
		NodePaths:           o.NodePaths,
		External:            o.External,
		EntryNames:          names,
		ChunkNames:          "chunks/[name]-[hash]",
		AssetNames:          "assets/[name]-[hash]",
		PublicPath:          o.PublicPath,
		Loader: map[string]api.Loader{
			".png": api.LoaderFile, ".jpg": api.LoaderFile, ".jpeg": api.LoaderFile, ".gif": api.LoaderFile,
			".svg": api.LoaderFile, ".webp": api.LoaderFile, ".avif": api.LoaderFile, ".ico": api.LoaderFile,
			".woff": api.LoaderFile, ".woff2": api.LoaderFile, ".ttf": api.LoaderFile, ".otf": api.LoaderFile,
		},
		Metafile: true,
		Write:    true,
		LogLevel: api.LogLevelSilent,
	}
	if dir := o.islandsDir(); dir != "" {
		b.Plugins = append(b.Plugins, o.islandsPlugin(dir))
	}
	if o.Navigate {
		b.Plugins = append(b.Plugins, navigatePlugin(o.Root))
	}
	if o.PWA != nil {
		b.Plugins = append(b.Plugins, pwaPlugin(o.Root))
	}
	if o.Dev {
		b.Sourcemap = api.SourceMapInline
	} else {
		b.MinifyWhitespace, b.MinifyIdentifiers, b.MinifySyntax = true, true, true
		b.Sourcemap = api.SourceMapLinked
	}
	return b
}

func (o Options) entryPoints() []api.EntryPoint {
	out := make([]api.EntryPoint, 0, len(o.Entries)+1)
	for _, e := range o.Entries {
		out = append(out, api.EntryPoint{InputPath: e})
	}
	if o.islandsDir() != "" {
		out = append(out, api.EntryPoint{InputPath: islandsNS, OutputPath: "islands"})
	}
	if o.Navigate {
		out = append(out, api.EntryPoint{InputPath: navigateNS, OutputPath: "navigate"})
	}
	if o.PWA != nil {
		out = append(out, api.EntryPoint{InputPath: pwaNS, OutputPath: "pwa"})
	}
	return out
}

// Asset is what an entry built into.
type Asset struct {
	File string   `json:"file"`          // the URL of the entry's output
	CSS  []string `json:"css,omitempty"` // CSS a JS entry imports (its CSS bundle)
}

// Manifest maps entries ("resources/js/app.tsx") to their built files. It
// is written to OutDir/manifest.json.
type Manifest struct {
	Entries map[string]Asset `json:"entries"`
	// Islands are the names of the island components, when there are any;
	// the loader is Entries[IslandsEntry].
	Islands []string `json:"islands,omitempty"`
	// Files are the URLs of every file of the build (not source maps): what
	// the service worker caches (Options.PWA).
	Files []string `json:"files,omitempty"`
}

// ManifestFile is the manifest's name in OutDir.
const ManifestFile = "manifest.json"

// Build builds the entries into OutDir and writes the manifest. Errors of
// the sources (syntax, missing imports) are returned together, with file
// and line.
func Build(ctx context.Context, o Options) (*Manifest, error) {
	o, err := o.withDefaults()
	if err != nil {
		return nil, err
	}
	if !o.Dev {
		if err := clean(o.outDir()); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res := api.Build(o.esbuild())
	return finish(o, res)
}

// clean empties dir but keeps .gitkeep: a committed placeholder lets
// //go:embed all:public/build compile in a fresh checkout, before any build.
func clean(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".gitkeep" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func finish(o Options, res api.BuildResult) (*Manifest, error) {
	if len(res.Errors) > 0 {
		return nil, buildError(res.Errors)
	}
	m, err := manifestOf(o, res.Metafile)
	if err != nil {
		return nil, err
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(o.outDir(), 0o755); err != nil {
		return nil, err
	}
	return m, os.WriteFile(filepath.Join(o.outDir(), ManifestFile), append(data, '\n'), 0o644)
}

// BuildError is a failed build: the messages of esbuild, with file and line.
type BuildError struct{ Messages []string }

func (e *BuildError) Error() string {
	return "bundler: build failed:\n" + strings.Join(e.Messages, "\n")
}

func buildError(msgs []api.Message) error {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Location != nil {
			out = append(out, fmt.Sprintf("%s:%d:%d: %s", m.Location.File, m.Location.Line, m.Location.Column, m.Text))
		} else {
			out = append(out, m.Text)
		}
	}
	return &BuildError{Messages: out}
}

type metafile struct {
	Outputs map[string]struct {
		EntryPoint string                     `json:"entryPoint"`
		CSSBundle  string                     `json:"cssBundle"`
		Inputs     map[string]json.RawMessage `json:"inputs"`
	} `json:"outputs"`
}

func manifestOf(o Options, meta string) (*Manifest, error) {
	var mf metafile
	if err := json.Unmarshal([]byte(meta), &mf); err != nil {
		return nil, fmt.Errorf("bundler: reading esbuild's metafile: %w", err)
	}
	url := func(out string) string {
		rel, _ := filepath.Rel(o.OutDir, filepath.FromSlash(out))
		return o.PublicPath + filepath.ToSlash(rel)
	}
	m := &Manifest{Entries: map[string]Asset{}}
	// An image or font given as an entry ("resources/icon.svg") is copied
	// as a file; esbuild also writes a JS module for it, which is skipped.
	copied := map[string]string{}
	for out, info := range mf.Outputs {
		ext := path.Ext(out)
		if ext != ".js" && ext != ".css" && ext != ".map" && len(info.Inputs) == 1 {
			for in := range info.Inputs {
				copied[in] = url(out)
			}
		}
	}
	for out, info := range mf.Outputs {
		if f, ok := copied[info.EntryPoint]; ok && path.Ext(out) == ".js" {
			m.Entries[filepath.ToSlash(info.EntryPoint)] = Asset{File: f}
			continue
		}
		if strings.HasSuffix(out, ".map") {
			continue
		}
		m.Files = append(m.Files, url(out))
		if info.EntryPoint == "" {
			continue
		}
		a := Asset{File: url(out)}
		if info.CSSBundle != "" {
			a.CSS = []string{url(info.CSSBundle)}
		}
		switch ep := info.EntryPoint; {
		case strings.HasPrefix(ep, islandNS+":"):
			m.Islands = append(m.Islands, strings.TrimPrefix(ep, islandNS+":"))
		case strings.HasPrefix(ep, islandsNS+":"):
			m.Entries[IslandsEntry] = a
		case ep == navigateNS+":"+navigateNS:
			m.Entries[NavigateEntry] = a
		case ep == pwaNS+":"+pwaNS:
			m.Entries[PWAEntry] = a
		case strings.HasPrefix(ep, navigateNS+":"): // its chunks
		default:
			m.Entries[filepath.ToSlash(ep)] = a
		}
	}
	sort.Strings(m.Islands)
	sort.Strings(m.Files)
	return m, nil
}

// LoadManifest reads OutDir/manifest.json from fsys (an embed.FS of the
// build, or os.DirFS).
func LoadManifest(fsys fs.FS, name string) (*Manifest, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("bundler: %w (build the assets first: go run . assets:build)", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("bundler: %s: %w", name, err)
	}
	return &m, nil
}

// Asset returns what entry built into.
func (m *Manifest) Asset(entry string) (Asset, error) {
	a, ok := m.Entries[path.Clean(filepath.ToSlash(entry))]
	if !ok {
		names := make([]string, 0, len(m.Entries))
		for n := range m.Entries {
			names = append(names, n)
		}
		sort.Strings(names)
		return Asset{}, fmt.Errorf("bundler: %q is not an entry (entries: %s)", entry, strings.Join(names, ", "))
	}
	return a, nil
}

// Tags returns the HTML tags of entries: a stylesheet link for CSS (also
// the CSS a JS entry imports) and a module script for JS.
func (m *Manifest) Tags(entries ...string) (template.HTML, error) {
	var b strings.Builder
	for _, e := range entries {
		a, err := m.Asset(e)
		if err != nil {
			return "", err
		}
		for _, css := range a.CSS {
			fmt.Fprintf(&b, `<link rel="stylesheet" href="%s">`, template.HTMLEscapeString(css))
		}
		if strings.HasSuffix(a.File, ".css") {
			fmt.Fprintf(&b, `<link rel="stylesheet" href="%s">`, template.HTMLEscapeString(a.File))
		} else {
			fmt.Fprintf(&b, `<script type="module" src="%s"></script>`, template.HTMLEscapeString(a.File))
		}
	}
	return template.HTML(b.String()), nil
}

// Watch builds for development and rebuilds whenever a source changes,
// until ctx is done; onBuild gets every result (a manifest, or the error to
// show). The first build happens before Watch returns its first callback.
func Watch(ctx context.Context, o Options, onBuild func(*Manifest, error)) error {
	return watch(ctx, o, func(_ *api.BuildResult, m *Manifest, err error) { onBuild(m, err) })
}

// watch is Watch that also passes esbuild's result (the output files and
// their hashes, for live reload).
func watch(ctx context.Context, o Options, onBuild func(*api.BuildResult, *Manifest, error)) error {
	o.Dev = true
	o, err := o.withDefaults()
	if err != nil {
		return err
	}
	b := o.esbuild()
	var mu sync.Mutex
	b.Plugins = append(b.Plugins, api.Plugin{Name: "fullstack-manifest", Setup: func(pb api.PluginBuild) {
		pb.OnEnd(func(res *api.BuildResult) (api.OnEndResult, error) {
			mu.Lock()
			defer mu.Unlock()
			m, err := finish(o, *res)
			onBuild(res, m, err)
			return api.OnEndResult{}, nil
		})
	}})
	bctx, cerr := api.Context(b)
	if cerr != nil {
		return buildError(cerr.Errors)
	}
	defer bctx.Dispose()
	if err := bctx.Watch(api.WatchOptions{Delay: 50}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
