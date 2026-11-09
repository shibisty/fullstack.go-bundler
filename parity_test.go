package bundler

import (
	"encoding/json"
	"html/template"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"fullstack/view"
)

// The browser's template engine (client/templates.js) must write what
// html/template writes. Each case is rendered by Go and by the JS engine
// in Node (skipped when Node is not installed; CI has it).

type parityUser struct {
	Name   string
	Email  string
	Secret string `json:"-"`
	Tags   []string
	Boss   *parityUser
	Embedded
}

type Embedded struct{ Team string }

type level int

func (l level) String() string { return [...]string{"low", "high"}[l] }

func parityFS() fstest.MapFS {
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	return fstest.MapFS{
		"v/layouts/app.html": f(`{{define "layout"}}<!doctype html><html><head><title>{{block "title" .}}App{{end}}</title>{{assets "app.ts"}}</head>` +
			`<body>{{template "nav" .}}<main>{{block "content" .}}{{end}}</main></body></html>{{end}}`),
		"v/partials/nav.html":  f(`{{define "nav"}}<nav>{{with .User}}<b>{{.Name}}</b>{{else}}guest{{end}}</nav>{{end}}`),
		"v/partials/card.html": f(`{{define "card"}}<div class="card {{.Class}}">{{.Title}}{{if .Body}}<p>{{.Body}}</p>{{end}}</div>{{end}}`),
		"v/pages/contexts.html": f(`{{define "title"}}{{.T}} · App{{end}}{{define "content"}}` +
			`<p title="{{.T}}" data-x={{.T}}>{{.T}}</p>` +
			`<a href="{{.URL}}">u</a><a href="/s?q={{.T}}&n={{.N}}">q</a><a href="{{.Safe}}">safe</a><a href="/p/{{.T}}">p</a>` +
			`<button onclick="go({{.T}}, {{.N}}, {{.F}}, {{.B}}, {{.L}}, {{.M}})">b</button>` +
			`<script>var a = {{.T}}; var n = {{.N}}; var l = {{.L}}; var m = {{.M}}; var s = "{{.T}}"; var r = /{{.T}}/; var h = {{.JS}};</script>` +
			`<style>p { color: {{.Color}}; background: {{.T}}; }</style><p style="color: {{.Color}}">s</p>` +
			`<img srcset="{{.Srcset}}" alt="{{.Uni}}"><textarea>{{.T}}</textarea><!-- {{.T}} -->` +
			`<div>{{.H}}</div><a href="{{.U}}">typed</a>{{end}}`),
		"v/pages/control.html": f(`{{define "content"}}` +
			`{{if .B}}yes{{else if .N}}n{{else}}no{{end}}|{{if not .Missing}}missing{{end}}|{{if and .B .N}}and{{end}}|{{or .Missing "dflt"}}|` +
			`{{range $i, $e := .L}}[{{$i}}:{{$e}}]{{else}}empty{{end}}|{{range .Empty}}x{{else}}none{{end}}|` +
			`{{range $k, $v := .M}}{{$k}}={{$v}};{{end}}|{{range 3}}{{.}}{{end}}|` +
			`{{range .L}}{{if eq . 2}}{{continue}}{{end}}{{if gt . 3}}{{break}}{{end}}{{.}}{{end}}|` +
			`{{$x := 1}}{{range .L}}{{$x = .}}{{end}}last={{$x}}|` +
			`{{with .User}}{{.Name}} {{.Team}} {{.Boss.Name}} {{len .Tags}}{{index .Tags 1}}{{end}}|` +
			`{{with .Missing}}no{{else with .N}}n={{.}}{{end}}|` +
			`{{template "card" dict "Class" "big" "Title" .T "Body" ""}}{{template "card" dict "Class" .Color "Title" "x" "Body" .H}}|` +
			`{{range .Users}}{{.Name}}{{if .Boss}}<{{.Boss.Name}}{{end}},{{end}}{{end}}`),
		"v/pages/funcs.html": f(`{{define "content"}}` +
			`{{printf "%d|%5d|%-4d|%05d|%s|%q|%v|%x|%X|%t|%.2f|%6.1f|%%" .N .N .N .N .T .T .L .N .T .B .F .F}}|` +
			`{{print .N .N "s" .F}}|{{println .N "x"}}|{{len .Uni}}|{{slice .Ascii 1 3}}|{{index .M "b"}}|` +
			`{{eq .N 7}}{{eq .T "x" "y"}}{{ne .N 3}}{{lt .N 9}}{{le .F 2.5}}{{gt "b" "a"}}{{ge .N 8}}|` +
			`{{html .T}}|{{js .T}}|{{urlquery .T " x"}}|{{.Level}}|{{.When}}|{{.Floats}}|{{.Ints}}|{{printf "%v %d" (index .Ints 1) (index .Ints 1)}}|{{.Nil}}|{{.Missing}}|` +
			`{{island "Counter" .Props}}{{island "Users" .Users}}<script>var u = {{.User}};</script>{{island "Chart" .L "visible" .H}}{{island "Bare"}}{{end}}`),
		"v/pages/plain.html":   f(`<p>{{.T}}</p>{{/* a comment */}}{{- " trimmed " -}}`),
		"v/pages/server.html":  f(`{{define "content"}}{{shout .T}}{{end}}`),
		"v/pages/method.html":  f(`{{define "content"}}{{.When.Year}}{{end}}`),
		"v/pages/pipe.html":    f(`{{define "content"}}{{"app.ts" | assets}}{{end}}`),
		"v/pages/missing.html": f(`{{define "content"}}{{.Nobody.Name}}{{end}}`),
	}
}

func parityEngine(t *testing.T) *view.Engine {
	t.Helper()
	e, err := view.New(parityFS(), view.Options{Dir: "v",
		Funcs: template.FuncMap{
			"assets": func(names ...string) template.HTML {
				return template.HTML(`<script type="module" src="/build/` + strings.Join(names, ",") + `"></script>`)
			},
			"island": func(name string, args ...any) (template.HTML, error) { return Island(nil, name, args...) },
			"shout":  strings.ToUpper,
		},
		ClientFuncs: (&Assets{}).ClientFuncs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func parityData() map[string]any {
	boss := &parityUser{Name: "Bo<ss>"}
	ann := &parityUser{Name: `Ann "A" & co`, Email: "ann@x", Secret: "s3cret", Tags: []string{"a", "b"}, Boss: boss, Embedded: Embedded{Team: "core"}}
	return map[string]any{
		"T":      `a"<b>'&+= x/y`,
		"N":      7,
		"F":      2.5,
		"B":      true,
		"L":      []int{1, 2, 3, 4},
		"Empty":  []string{},
		"M":      map[string]any{"b": 2, "a": "x<y", "c": []string{"p", "q"}},
		"URL":    "javascript:alert(1)",
		"Safe":   "https://example.com/a b?x=ü&y=1",
		"U":      template.URL("javascript:void(0)"),
		"H":      template.HTML("<i>raw</i>"),
		"JS":     template.JS("1 + 2"),
		"Color":  "red",
		"Srcset": "/a.png 1x, javascript:x 2x, /b c.png 3x",
		"Uni":    "Привет ✓ 😀",
		"Ascii":  "hello",
		"User":   ann,
		"Users":  []*parityUser{ann, boss},
		"Level":  level(1),
		"When":   time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC),
		"Floats": []float64{0.00001, 1e21, 123456789, 0.1, -0, 3, 999999, 1e6, 1234567.5, 0.00012345},
		"Ints":   []int64{1e6, 1 << 60, -5},
		"Nil":    nil,
		"Props":  map[string]any{"start": 3, "label": "</script>"},
	}
}

func TestBrowserTemplatesParity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	e := parityEngine(t)
	data := parityData()
	w := httptest.NewRecorder()
	e.ServePack(w, httptest.NewRequest("GET", "/__fullstack/templates", nil))
	pack := w.Body.Bytes()
	if strings.Contains(string(pack), "s3cret") {
		t.Fatal("pack holds data")
	}

	type tcase struct {
		Page string          `json:"page"`
		Data json.RawMessage `json:"data"`
	}
	var cases []tcase
	want := map[string]string{}
	for _, page := range []string{"pages/contexts", "pages/control", "pages/funcs", "pages/plain", "pages/method", "pages/missing"} {
		html, err := e.RenderString(page, data)
		if err != nil && page != "pages/missing" {
			t.Fatal(page, err)
		}
		want[page] = html
		cd, ok := e.ClientData(page, data)
		if !ok {
			t.Fatal(page, "not rendered in the browser")
		}
		if strings.Contains(string(cd), "s3cret") {
			t.Fatal("json:\"-\" field sent")
		}
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		json.Unmarshal(cd, &env)
		cases = append(cases, tcase{page, env.Data})
	}
	for _, page := range []string{"pages/server", "pages/pipe"} {
		if _, ok := e.ClientData(page, data); ok {
			t.Errorf("%s: rendered in the browser", page)
		}
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "templates.mjs"), []byte(templatesScript), 0o644)
	os.WriteFile(filepath.Join(dir, "pack.json"), pack, 0o644)
	cj, _ := json.Marshal(cases)
	os.WriteFile(filepath.Join(dir, "cases.json"), cj, 0o644)
	os.WriteFile(filepath.Join(dir, "run.mjs"), []byte(`
import { renderPage, Unsupported } from "./templates.mjs";
import { readFileSync } from "node:fs";
const pack = JSON.parse(readFileSync(new URL("./pack.json", import.meta.url)));
const cases = JSON.parse(readFileSync(new URL("./cases.json", import.meta.url)));
const out = {};
for (const c of cases) {
  try { out[c.page] = renderPage(pack, c.page, c.data); }
  catch (e) { out[c.page] = e instanceof Unsupported ? "UNSUPPORTED: " + e.message : "ERROR: " + e.stack; }
}
console.log(JSON.stringify(out));
`), 0o644)
	cmd := exec.Command(node, filepath.Join(dir, "run.mjs"))
	res, err := cmd.Output()
	if err != nil {
		t.Fatal(err, string(res))
	}
	var got map[string]string
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatal(err, string(res))
	}
	for _, c := range cases {
		switch c.Page {
		case "pages/method", "pages/missing":
			if !strings.HasPrefix(got[c.Page], "UNSUPPORTED") {
				t.Errorf("%s: %s", c.Page, got[c.Page])
			}
			continue
		}
		if got[c.Page] != want[c.Page] {
			t.Errorf("%s\n go: %s\n js: %s", c.Page, want[c.Page], got[c.Page])
		}
	}
}
