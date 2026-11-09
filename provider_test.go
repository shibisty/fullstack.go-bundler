package bundler

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"fullstack"
)

func newApp(t *testing.T, appEnv string) (*fullstack.App, *bytes.Buffer) {
	t.Helper()
	for _, k := range []string{"APP_ENV", "ASSETS_WATCH"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("APP_ENV", appEnv)
	app, err := fullstack.New(fullstack.Options{EnvFiles: []string{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app.Stdout, app.Stderr = &out, &out
	return app, &out
}

func get(t *testing.T, app *fullstack.App, path string) *httptest.ResponseRecorder {
	t.Helper()
	h, err := app.Handler()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestProviderProduction(t *testing.T) {
	dir := project(t)
	app, out := newApp(t, "production")
	p := &Provider{Options: Options{Root: dir, Entries: entries}}
	if err := app.Register(p); err != nil {
		t.Fatal(err)
	}
	// Without a build, booting fails with the command to run.
	if err := app.Boot(); err == nil || !strings.Contains(err.Error(), "assets:build") {
		t.Fatal(err)
	}
	app, out = newApp(t, "production")
	p = &Provider{Options: Options{Root: dir, Entries: entries}}
	app.Register(p)
	if code, _ := app.Console([]string{"assets:build"}); code != 0 || !strings.Contains(out.String(), "Built 2 entries into public/build") {
		t.Fatal(code, out)
	}
	assets := fullstack.MustMake[*Assets](app)
	w := get(t, app, "/") // boots
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	m, err := assets.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	js := m.Entries["resources/js/app.tsx"].File
	w = get(t, app, js)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") || !strings.Contains(w.Body.String(), "Hello, ") {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	if w := get(t, app, "/build/manifest.json"); w.Code != 404 {
		t.Fatal("manifest served")
	}
	if w := get(t, app, "/build/js/"); w.Code != 404 {
		t.Fatal("directory listing")
	}
	tags, err := assets.Tags("resources/js/app.tsx")
	if err != nil || !strings.Contains(string(tags), js) {
		t.Fatal(tags, err)
	}
	if u, err := assets.URL("resources/css/app.css"); err != nil || !strings.HasPrefix(u, "/build/css/app-") {
		t.Fatal(u, err)
	}
	if _, err := assets.URL("nope.ts"); err == nil {
		t.Fatal("unknown entry")
	}
	if _, err := assets.Tags("nope.ts"); err == nil {
		t.Fatal("unknown entry")
	}
	if len(assets.Funcs()) != 3 {
		t.Fatal("funcs")
	}

	// The same build embedded (any fs.FS) instead of read from disk.
	emb := newEmbedded(t, filepath.Join(dir, "public/build"))
	os.RemoveAll(filepath.Join(dir, "public/build"))
	app2, _ := newApp(t, "production")
	app2.Register(&Provider{Options: Options{Root: dir, Entries: entries}, Build: emb})
	if w := get(t, app2, js); w.Code != 200 {
		t.Fatal("embedded build", w.Code)
	}
	if code, _ := app2.Console([]string{"assets:build"}); code != 0 {
		t.Fatal("rebuild")
	}
	out.Reset()
	bad, bout := newApp(t, "production")
	bad.Register(&Provider{Options: Options{Root: dir}})
	if code, _ := bad.Console([]string{"assets:build"}); code != 1 || !strings.Contains(bout.String(), "no entries") {
		t.Fatal(code, bout)
	}
}

// newEmbedded copies a directory into a MapFS, as an embed.FS would hold it.
func newEmbedded(t *testing.T, dir string) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			data, _ := os.ReadFile(p)
			m[filepath.ToSlash(rel)] = &fstest.MapFile{Data: data}
		}
		return err
	})
	return m
}

func TestProviderTesting(t *testing.T) {
	// APP_ENV=testing without a build: pages render without asset tags.
	app, _ := newApp(t, "testing")
	app.Register(&Provider{Options: Options{Root: t.TempDir(), Entries: entries}})
	get(t, app, "/")
	a := fullstack.MustMake[*Assets](app)
	if tags, err := a.Tags("resources/js/app.tsx"); err != nil || tags != "" {
		t.Fatal(tags, err)
	}
	if u, err := a.URL("x"); err != nil || u != "" {
		t.Fatal(u, err)
	}
	// With a build, an unknown entry is still not an error in tests.
	dir := project(t)
	if _, err := Build(context.Background(), Options{Root: dir, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	app, _ = newApp(t, "testing")
	app.Register(&Provider{Options: Options{Root: dir, Entries: entries}})
	get(t, app, "/")
	a = fullstack.MustMake[*Assets](app)
	if tags, err := a.Tags("nope.ts"); err != nil || tags != "" {
		t.Fatal(tags, err)
	}
	if u, err := a.URL("nope.ts"); err != nil || u != "" {
		t.Fatal(u, err)
	}
}

func TestProviderWatch(t *testing.T) {
	dir := project(t)
	app, _ := newApp(t, "local") // ASSETS_WATCH defaults to on
	app.Register(&Provider{Options: Options{Root: dir, Entries: entries}})
	w := get(t, app, "/build/js/app.js")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(w.Body.String(), "Hello, ") {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	a := fullstack.MustMake[*Assets](app)
	os.WriteFile(filepath.Join(dir, "resources/js/greet.ts"), []byte("export function greet( {\n"), 0o644)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := a.Manifest(); err != nil {
			break // the broken build is reported
		}
		if time.Now().After(deadline) {
			t.Fatal("no rebuild")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A broken build writes only the live reload script, so the page
	// renders and shows the errors.
	if tags, err := a.Tags("resources/js/app.tsx"); err != nil || string(tags) != `<script type="module" src="/build/__live.js"></script>` {
		t.Fatal(tags, err)
	}
	if _, err := a.URL("resources/js/app.tsx"); err == nil {
		t.Fatal("URL of a broken build")
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A watcher that cannot start reports it.
	app, _ = newApp(t, "local")
	app.Register(&Provider{Options: Options{Root: dir}})
	get(t, app, "/")
	if _, err := fullstack.MustMake[*Assets](app).Manifest(); err == nil {
		t.Fatal("watch without entries")
	}
}

func TestNoBuildYet(t *testing.T) {
	a := &Assets{built: make(chan struct{})}
	if _, err := a.Manifest(); err == nil || !strings.Contains(err.Error(), "no build yet") {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Sub with a bad name")
		}
	}()
	Sub(fstest.MapFS{}, "../x")
}

// A console command boots the app without watching or needing a build.
func TestProviderConsole(t *testing.T) {
	dir := project(t)
	for _, env := range []string{"local", "production"} {
		app, out := newApp(t, env)
		app.Register(&Provider{Options: Options{Root: dir, Entries: entries}})
		if code, _ := app.Console([]string{"routes:list"}); code != 0 || !strings.Contains(out.String(), "/build/") {
			t.Fatal(env, code, out)
		}
		if !app.InConsole() {
			t.Fatal("InConsole")
		}
		if _, err := os.Stat(filepath.Join(dir, "public/build")); !os.IsNotExist(err) {
			t.Fatal(env, "built in a console command", err)
		}
	}
}
