package bundler

import (
	"context"
	"html/template"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"fullstack"
)

// islandsProject is the test project with islands.
func islandsProject(t *testing.T) string {
	t.Helper()
	dir := project(t)
	files := map[string]string{
		"resources/islands/Counter.tsx":     "import { label } from \"./_util\";\nexport default function Counter(props: { start: number }) {\n  return <button>{label(props.start)}</button>;\n}\n",
		"resources/islands/_util.ts":        "export const label = (n: number) => `count ${n}`;\n",
		"resources/islands/Counter.test.ts": "throw new Error(\"not an island\");\n",
		"resources/islands/types.d.ts":      "declare const x: number;\n",
		"resources/islands/charts/Bar.jsx":  "export default function Bar() { return <svg />; }\n",
		"resources/islands/_drafts/Old.tsx": "syntax error (\n",
		"resources/islands/.hidden/Hid.tsx": "syntax error (\n",
		"resources/islands/notes/README.md": "not code\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestIslandsBuild(t *testing.T) {
	dir := islandsProject(t)
	m, err := Build(context.Background(), Options{Root: dir, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Islands, []string{"Counter", "charts/Bar"}) {
		t.Fatal(m.Islands)
	}
	loader := m.Entries[IslandsEntry].File
	if !strings.HasPrefix(loader, "/build/islands-") || !strings.HasSuffix(loader, ".js") {
		t.Fatalf("%+v", m.Entries)
	}
	if _, ok := m.Entries["resources/js/app.tsx"]; !ok {
		t.Fatalf("%+v", m.Entries)
	}
	data, err := os.ReadFile(filepath.Join(dir, "public", filepath.FromSlash(loader)))
	if err != nil {
		t.Fatal(err)
	}
	// Each island is its own chunk, loaded on demand; helpers are not islands.
	js := string(data)
	if !strings.Contains(js, `"charts/Bar"`) || !strings.Contains(js, "fullstack-island") || strings.Contains(js, "count ") || strings.Contains(js, "_util") {
		t.Fatal(js)
	}
	chunks, _ := filepath.Glob(filepath.Join(dir, "public/build/chunks/*.js"))
	var counter bool
	for _, c := range chunks {
		b, _ := os.ReadFile(c)
		counter = counter || strings.Contains(string(b), "count ")
	}
	if !counter {
		t.Fatal("no chunk with the Counter island", chunks)
	}
	tags, err := m.Tags(IslandsEntry)
	if err != nil || string(tags) != `<script type="module" src="`+loader+`"></script>` {
		t.Fatal(tags, err)
	}

	// Development: a stable name; only islands, no other entries, is fine.
	m, err = Build(context.Background(), Options{Root: dir, Dev: true})
	if err != nil || m.Entries[IslandsEntry].File != "/build/islands.js" || len(m.Entries) != 1 {
		t.Fatalf("%+v %v", m, err)
	}
	// Islands off, or no folder: no loader.
	m, err = Build(context.Background(), Options{Root: dir, Entries: entries, Islands: "-"})
	if err != nil || len(m.Islands) != 0 || m.Entries[IslandsEntry].File != "" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := Build(context.Background(), Options{Root: dir, Islands: "nowhere"}); err == nil || !strings.Contains(err.Error(), "no entries") {
		t.Fatal(err)
	}
}

func TestIslandErrors(t *testing.T) {
	dir := islandsProject(t)
	os.WriteFile(filepath.Join(dir, "resources/islands/Counter.jsx"), []byte("export default () => null;\n"), 0o644)
	if _, err := Build(context.Background(), Options{Root: dir, Dev: true}); err == nil || !strings.Contains(err.Error(), `island "Counter" is both`) {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "resources/islands/Counter.jsx"))
	os.WriteFile(filepath.Join(dir, "resources/islands/my island.tsx"), []byte("export default () => null;\n"), 0o644)
	if _, err := Build(context.Background(), Options{Root: dir, Dev: true}); err == nil || !strings.Contains(err.Error(), "names may hold") {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "resources/islands/my island.tsx"))
	// A component without a default export.
	os.WriteFile(filepath.Join(dir, "resources/islands/Named.tsx"), []byte("export function Named() { return null; }\n"), 0o644)
	if _, err := Build(context.Background(), Options{Root: dir, Dev: true}); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatal(err)
	}
}

func TestIslandModule(t *testing.T) {
	o := Options{JSXImportSource: "preact"}
	if src := o.islandModule("/x/A.tsx"); !strings.Contains(src, `import { h, render } from "preact"`) || !strings.Contains(src, `import C from "/x/A.tsx"`) {
		t.Fatal(src)
	}
	o.JSXImportSource = "react"
	if src := o.islandModule(`C:\x\A.tsx`); !strings.Contains(src, `from "react-dom/client"`) || !strings.Contains(src, `import C from "C:\\x\\A.tsx"`) {
		t.Fatal(src)
	}
}

func TestIslandElement(t *testing.T) {
	known := map[string]bool{"Counter": true, "charts/Bar": true}
	cases := []struct {
		args []any
		want string
	}{
		{nil, `<fullstack-island data-island="Counter" style="display:contents"></fullstack-island>`},
		{[]any{map[string]any{"start": 3, "label": `"</script>&`}},
			`<fullstack-island data-island="Counter" data-props="{&#34;label&#34;:&#34;\&#34;\u003c/script\u003e\u0026&#34;,&#34;start&#34;:3}" style="display:contents"></fullstack-island>`},
		{[]any{nil, "visible"}, `<fullstack-island data-island="Counter" data-hydrate="visible" style="display:contents"></fullstack-island>`},
		{[]any{struct{ N int }{1}, "load", template.HTML("<b>0</b>")}, `<fullstack-island data-island="Counter" data-props="{&#34;N&#34;:1}" style="display:contents"><b>0</b></fullstack-island>`},
		{[]any{nil, "idle", "<i>wait</i>"}, `<fullstack-island data-island="Counter" data-hydrate="idle" style="display:contents">&lt;i&gt;wait&lt;/i&gt;</fullstack-island>`},
	}
	for _, c := range cases {
		got, err := Island(known, "Counter", c.args...)
		if err != nil || string(got) != c.want {
			t.Errorf("%v:\n%s\n%s %v", c.args, got, c.want, err)
		}
	}
	if _, err := Island(known, "Nope"); err == nil || !strings.Contains(err.Error(), "islands: Counter, charts/Bar") {
		t.Fatal(err)
	}
	if _, err := Island(map[string]bool{}, "Nope"); err == nil || !strings.Contains(err.Error(), "islands: none") {
		t.Fatal(err)
	}
	if _, err := Island(nil, "../x"); err == nil {
		t.Fatal("bad name")
	}
	if _, err := Island(nil, "Nope", nil, 5); err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatal(err)
	}
	if _, err := Island(nil, "Nope", func() {}); err == nil || !strings.Contains(err.Error(), "props") {
		t.Fatal(err)
	}
	if got, err := Island(nil, "charts/Bar"); err != nil || !strings.Contains(string(got), `data-island="charts/Bar"`) {
		t.Fatal(got, err)
	}
}

func TestIslandsInApp(t *testing.T) {
	dir := islandsProject(t)
	app, _ := newApp(t, "local")
	app.Register(&Provider{Options: Options{Root: dir, Entries: entries}})
	get(t, app, "/")
	a := fullstack.MustMake[*Assets](app)
	tags, err := a.Tags("resources/css/app.css")
	if err != nil || !strings.Contains(string(tags), `<script type="module" src="/build/islands.js"></script>`) || !strings.Contains(string(tags), "__live.js") {
		t.Fatal(tags, err)
	}
	if tags, _ := a.Tags(IslandsEntry); strings.Count(string(tags), "islands.js") != 1 {
		t.Fatal(tags)
	}
	if _, err := a.Island("Counter", map[string]int{"start": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Island("Nope"); err == nil {
		t.Fatal("unknown island")
	}
	// A new island is picked up by the watcher.
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "resources/islands/Clock.tsx"), []byte("export default () => <time />;\n"), 0o644)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := a.Island("Clock"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			m, err := a.Manifest()
			t.Fatal("new island not built", m, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	app.Shutdown(context.Background())

	// In tests names are not checked and there is no build.
	app, _ = newApp(t, "testing")
	app.Register(&Provider{Options: Options{Root: t.TempDir(), Entries: entries}})
	get(t, app, "/")
	if _, err := fullstack.MustMake[*Assets](app).Funcs()["island"].(func(string, ...any) (template.HTML, error))("Anything"); err != nil {
		t.Fatal(err)
	}
}

func TestNavigate(t *testing.T) {
	dir := islandsProject(t)
	m, err := Build(context.Background(), Options{Root: dir, Entries: entries, Navigate: true})
	if err != nil {
		t.Fatal(err)
	}
	nav := m.Entries[NavigateEntry].File
	if !strings.HasPrefix(nav, "/build/navigate-") {
		t.Fatalf("%+v", m.Entries)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "public", filepath.FromSlash(nav)))
	if !strings.Contains(string(data), "fullstackNavigate") || !strings.Contains(string(data), "X-Fullstack-Navigate") {
		t.Fatal(string(data))
	}
	// Alone, without entries or islands, and added by Assets.Tags.
	if m, err := Build(context.Background(), Options{Root: t.TempDir(), Navigate: true, Dev: true}); err != nil || m.Entries[NavigateEntry].File != "/build/navigate.js" {
		t.Fatal(m, err)
	}
	app, _ := newApp(t, "local")
	app.Register(&Provider{Options: Options{Root: dir, Entries: entries, Navigate: true}})
	get(t, app, "/")
	tags, _ := fullstack.MustMake[*Assets](app).Tags("resources/css/app.css")
	if !strings.Contains(string(tags), `src="/build/navigate.js"`) || !strings.Contains(string(tags), `src="/build/islands.js"`) {
		t.Fatal(tags)
	}
	app.Shutdown(context.Background())
	if src := (Options{JSXImportSource: "preact"}).islandModule("/a.tsx"); !strings.Contains(src, "return () => render(null, el)") {
		t.Fatal(src)
	}
}
