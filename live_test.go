package bundler

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fullstack"
)

// stream reads Server-Sent Events.
type stream struct {
	t      *testing.T
	body   io.Closer
	events chan [2]string
}

func openStream(t *testing.T, url string) *stream {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Status, resp.Header)
	}
	s := &stream{t: t, body: resp.Body, events: make(chan [2]string, 16)}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		var name, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && name != "":
				s.events <- [2]string{name, data}
				name, data = "", ""
			}
		}
	}()
	t.Cleanup(func() { resp.Body.Close() })
	return s
}

func (s *stream) next() [2]string {
	s.t.Helper()
	select {
	case e, ok := <-s.events:
		if !ok {
			s.t.Fatal("stream closed")
		}
		return e
	case <-time.After(10 * time.Second):
		s.t.Fatal("no event")
	}
	return [2]string{}
}

func TestLiveReload(t *testing.T) {
	dir := project(t)
	app, _ := newApp(t, "local")
	app.Register(&Provider{Options: Options{Root: dir, Entries: entries}})
	h, err := app.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	a := fullstack.MustMake[*Assets](app)
	if tags, _ := a.Tags("resources/js/app.tsx"); !strings.HasSuffix(string(tags), `<script type="module" src="/build/__live.js"></script>`) {
		t.Fatal(tags)
	}
	resp, err := http.Get(srv.URL + "/build/__live.js")
	if err != nil {
		t.Fatal(err)
	}
	js, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(js), "new EventSource") || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Fatal(resp.Status, string(js))
	}

	s := openStream(t, srv.URL+"/build/__live")
	hello := s.next()
	if hello[0] != "hello" || len(hello[1]) != 16 {
		t.Fatal(hello)
	}
	write := func(name, body string) {
		t.Helper()
		time.Sleep(100 * time.Millisecond) // past esbuild's watch delay
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// CSS only: the stylesheets are swapped, a JS entry's CSS bundle too.
	write("resources/css/app.css", "body { margin: 1px; }\n")
	if e := s.next(); e[0] != "css" || e[1] != `["/build/css/app.css"]` {
		t.Fatal(e)
	}
	write("resources/css/widget.css", ".hello { color: red; }\n")
	if e := s.next(); e[0] != "css" || e[1] != `["/build/js/app.css"]` {
		t.Fatal(e)
	}
	// JavaScript: a reload.
	write("resources/js/greet.ts", "export function greet(name: string): string {\n  return `Hi, ${name}!`;\n}\n")
	if e := s.next(); e[0] != "reload" {
		t.Fatal(e)
	}
	// A failed build: its errors, also for a page that connects now.
	write("resources/js/greet.ts", "export function greet( {\n")
	if e := s.next(); e[0] != "failed" || !strings.Contains(e[1], "greet.ts:") {
		t.Fatal(e)
	}
	late := openStream(t, srv.URL+"/build/__live")
	if e := late.next(); e[0] != "hello" || e[1] != hello[1] {
		t.Fatal(e)
	}
	if e := late.next(); e[0] != "failed" {
		t.Fatal(e)
	}
	// Fixed: a reload.
	write("resources/js/greet.ts", "export function greet(name: string): string {\n  return name;\n}\n")
	if e := s.next(); e[0] != "reload" {
		t.Fatal(e)
	}
	// Shutdown ends the streams.
	start := time.Now()
	app.Shutdown(context.Background())
	for range s.events {
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("stream outlived shutdown")
	}
}

func TestLiveOff(t *testing.T) {
	dir := project(t)
	app, _ := newApp(t, "local")
	t.Setenv("ASSETS_LIVE", "false")
	app.Register(&Provider{Options: Options{Root: dir, Entries: entries}})
	h, err := app.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if tags, _ := fullstack.MustMake[*Assets](app).Tags("resources/js/app.tsx"); strings.Contains(string(tags), "__live") {
		t.Fatal(tags)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/build/__live.js", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	app.Shutdown(context.Background())
	// In production there is no live reload either.
	app, _ = newApp(t, "production")
	app.Register(&Provider{Options: Options{Root: dir, Entries: entries}})
	if _, err := Build(context.Background(), Options{Root: dir, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	if tags, _ := fullstack.MustMake[*Assets](app).Tags("resources/js/app.tsx"); strings.Contains(string(tags), "__live") {
		t.Fatal(tags)
	}
}
