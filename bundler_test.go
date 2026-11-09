package bundler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// project copies testdata/app into a temporary directory.
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := filepath.Walk("testdata/app", func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata/app", p)
		dst := filepath.Join(dir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

var entries = []string{"resources/js/app.tsx", "resources/css/app.css"}

func read(t *testing.T, dir, url string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "public/build", strings.TrimPrefix(url, "/build/")))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBuildProduction(t *testing.T) {
	dir := project(t)
	os.MkdirAll(filepath.Join(dir, "public/build/old"), 0o755)
	os.WriteFile(filepath.Join(dir, "public/build/old/stale.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "public/build/.gitkeep"), nil, 0o644)
	m, err := Build(context.Background(), Options{Root: dir, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	js, err := m.Asset("resources/js/app.tsx")
	if err != nil {
		t.Fatal(err)
	}
	hashed := regexp.MustCompile(`^/build/js/app-[A-Z0-9]{8}\.js$`)
	if !hashed.MatchString(js.File) || len(js.CSS) != 1 || !strings.HasPrefix(js.CSS[0], "/build/js/app-") {
		t.Fatalf("%+v", m.Entries)
	}
	code := read(t, dir, js.File)
	if !strings.Contains(code, "Hello, ") || strings.Contains(code, "loaded on demand") || strings.Contains(code, "function greet") {
		t.Fatalf("not bundled, split and minified:\n%s", code)
	}
	if !strings.Contains(code, "//# sourceMappingURL=") {
		t.Fatal("no source map link")
	}
	if css := read(t, dir, js.CSS[0]); !strings.Contains(css, "rebeccapurple") && !strings.Contains(css, "#639") {
		t.Fatalf("css bundle: %s", css)
	}
	app, _ := m.Asset("resources/css/app.css")
	css := read(t, dir, app.File)
	if !regexp.MustCompile(`url\("?/build/assets/logo-[A-Z0-9]{8}\.svg"?\)`).MatchString(css) {
		t.Fatalf("asset url: %s", css)
	}
	if _, err := os.Stat(filepath.Join(dir, "public/build/old/stale.js")); !os.IsNotExist(err) {
		t.Fatal("old files kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "public/build/.gitkeep")); err != nil {
		t.Fatal(".gitkeep removed")
	}
	// The manifest on disk is the same.
	loaded, err := LoadManifest(os.DirFS(filepath.Join(dir, "public/build")), ManifestFile)
	if err != nil || loaded.Entries["resources/js/app.tsx"].File != js.File {
		t.Fatal(err, loaded)
	}
	tags, err := m.Tags("resources/js/app.tsx", "resources/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	want := `<link rel="stylesheet" href="` + js.CSS[0] + `"><script type="module" src="` + js.File + `"></script><link rel="stylesheet" href="` + app.File + `">`
	if string(tags) != want {
		t.Fatalf("tags\n%s\nwant\n%s", tags, want)
	}
	if _, err := m.Tags("nope.ts"); err == nil || !strings.Contains(err.Error(), "entries: resources/css/app.css, resources/js/app.tsx") {
		t.Fatal(err)
	}
}

func TestBuildDev(t *testing.T) {
	dir := project(t)
	m, err := Build(context.Background(), Options{Root: dir, Entries: entries, Dev: true, PublicPath: "/assets", OutDir: "web/dist"})
	if err != nil {
		t.Fatal(err)
	}
	js := m.Entries["resources/js/app.tsx"]
	if js.File != "/assets/js/app.js" {
		t.Fatalf("%+v", m.Entries)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "web/dist/js/app.js"))
	if !strings.Contains(string(data), "function greet(") || !strings.Contains(string(data), "sourceMappingURL=data:") || !strings.Contains(string(data), `"development"`) && strings.Contains(string(data), "NODE_ENV") {
		t.Fatalf("dev build:\n%s", data)
	}
}

func TestBuildErrors(t *testing.T) {
	dir := project(t)
	os.WriteFile(filepath.Join(dir, "resources/js/broken.ts"), []byte("export const x = ;\nimport y from './missing';\n"), 0o644)
	_, err := Build(context.Background(), Options{Root: dir, Entries: []string{"resources/js/broken.ts"}})
	var be *BuildError
	if !errors.As(err, &be) || !strings.Contains(err.Error(), "resources/js/broken.ts:1:") || len(be.Messages) < 1 {
		t.Fatal(err)
	}
	for _, o := range []Options{
		{Root: dir},
		{Root: dir, Entries: entries, OutDir: "."},
		{Root: dir, Entries: entries, OutDir: "../elsewhere"},
	} {
		if _, err := Build(context.Background(), o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, Options{Root: dir, Entries: entries}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := LoadManifest(os.DirFS(dir), "nope.json"); err == nil || !strings.Contains(err.Error(), "assets:build") {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o644)
	if _, err := LoadManifest(os.DirFS(dir), "bad.json"); err == nil {
		t.Fatal("bad manifest")
	}
}

func TestWatch(t *testing.T) {
	dir := project(t)
	ctx, cancel := context.WithCancel(context.Background())
	builds := make(chan error, 10)
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, Options{Root: dir, Entries: entries}, func(m *Manifest, err error) { builds <- err })
	}()
	wait := func() error {
		select {
		case err := <-builds:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("no build")
		}
		return nil
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	// A change rebuilds; a syntax error is reported, its fix rebuilds again.
	src := filepath.Join(dir, "resources/js/greet.ts")
	os.WriteFile(src, []byte("export function greet(n: string) { return `Hi, ${n}`; }\n"), 0o644)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "public/build/js/app.js")); !strings.Contains(string(data), "Hi, ") {
		t.Fatal("not rebuilt")
	}
	os.WriteFile(src, []byte("export function greet( {\n"), 0o644)
	if err := wait(); err == nil {
		t.Fatal("syntax error not reported")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := Watch(context.Background(), Options{Root: dir}, nil); err == nil {
		t.Fatal("no entries")
	}
}
