package bundler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fullstack"
)

func TestPWA(t *testing.T) {
	dir := project(t)
	os.WriteFile(filepath.Join(dir, "resources/icon.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
	pwa := &PWA{ThemeColor: "#123456", Icons: []Icon{{Src: "resources/icon.svg", Sizes: "any", Type: "image/svg+xml"}, {Src: "/static/i.png", Sizes: "192x192"}}, Offline: "/offline"}
	opts := Options{Root: dir, Entries: append(entries, "resources/icon.svg"), PWA: pwa}

	// The production build: every file listed, the icon as its file.
	m, err := Build(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	icon := m.Entries["resources/icon.svg"].File
	if !strings.HasPrefix(icon, "/build/assets/icon-") || !strings.HasSuffix(icon, ".svg") || m.Entries[PWAEntry].File == "" {
		t.Fatalf("%+v", m.Entries)
	}
	for _, f := range m.Files {
		if strings.HasSuffix(f, ".map") || strings.Contains(f, "/icon-") && strings.HasSuffix(f, ".js") {
			t.Fatal("listed:", f)
		}
	}
	if len(m.Files) < 5 {
		t.Fatal(m.Files)
	}

	app, _ := newApp(t, "production")
	app.Register(&Provider{Options: opts})
	if w := get(t, app, "/manifest.webmanifest"); w.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatal(w.Header())
	} else {
		var wm map[string]any
		json.Unmarshal(w.Body.Bytes(), &wm)
		icons := wm["icons"].([]any)
		if wm["name"] == "" || wm["theme_color"] != "#123456" || wm["display"] != "standalone" ||
			icons[0].(map[string]any)["src"] != icon || icons[1].(map[string]any)["src"] != "/static/i.png" {
			t.Fatal(w.Body.String())
		}
	}
	w := get(t, app, "/sw.js")
	sw := w.Body.String()
	if w.Header().Get("Service-Worker-Allowed") != "/" || !strings.HasPrefix(sw, "self.__FULLSTACK = {") ||
		!strings.Contains(sw, `"/__fullstack/templates"`) || !strings.Contains(sw, `"/offline"`) || !strings.Contains(sw, icon) ||
		!strings.Contains(sw, `addEventListener("fetch"`) {
		t.Fatal(sw)
	}
	tags, _ := fullstack.MustMake[*Assets](app).Tags("resources/css/app.css")
	if !strings.HasPrefix(string(tags), `<link rel="manifest" href="/manifest.webmanifest"><meta name="theme-color" content="#123456">`) ||
		!strings.Contains(string(tags), m.Entries[PWAEntry].File) {
		t.Fatal(tags)
	}

	// Another build: another worker version.
	os.WriteFile(filepath.Join(dir, "resources/css/app.css"), []byte("body{margin:3px}"), 0o644)
	if _, err := Build(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	app2, _ := newApp(t, "production")
	app2.Register(&Provider{Options: opts})
	version := func(sw string) string { i := strings.Index(sw, `"version":"`); return sw[i : i+30] }
	if sw2 := get(t, app2, "/sw.js").Body.String(); version(sw2) == version(sw) {
		t.Fatal("same version", version(sw))
	}

	// Development: no registration, and a worker that removes itself.
	dev, _ := newApp(t, "local")
	dev.Register(&Provider{Options: opts})
	if sw := get(t, dev, "/sw.js").Body.String(); !strings.Contains(sw, "unregister()") || strings.Contains(sw, "__FULLSTACK") {
		t.Fatal(sw)
	}
	tags, _ = fullstack.MustMake[*Assets](dev).Tags("resources/css/app.css")
	if strings.Contains(string(tags), "/build/pwa") || !strings.Contains(string(tags), `rel="manifest"`) {
		t.Fatal(tags)
	}
	dev.Shutdown(context.Background())
}
