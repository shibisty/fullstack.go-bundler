package bundler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/evanw/esbuild/pkg/api"
)

// live is the development channel to the browser: Server-Sent Events at
// <PublicPath>__live and the client script at <PublicPath>__live.js, which
// Assets.Tags adds to pages while the provider watches.
//
// After a rebuild that changed only CSS the page swaps its stylesheets in
// place; any other change reloads it. A failed build shows its errors over
// the page until the next good build. Each process has its own boot id, so
// a page that reconnects to a restarted server (gtr dev rebuilt the Go
// program) reloads too.
type live struct {
	boot   string
	outDir string // absolute
	public string // PublicPath, ending in /
	stop   <-chan struct{}

	mu      sync.Mutex
	clients map[chan liveEvent]struct{}
	hashes  map[string]string // output file → esbuild's hash, after the last good build
	failed  *liveEvent        // the errors of the last build, if it failed
}

type liveEvent struct{ name, data string }

func newLive(outDir, public string, stop <-chan struct{}) *live {
	var b [8]byte
	rand.Read(b[:])
	return &live{boot: hex.EncodeToString(b[:]), outDir: outDir, public: public, stop: stop,
		clients: map[chan liveEvent]struct{}{}}
}

// built turns a build into the event for the pages: nothing for the first
// build or one that changed nothing, "css" with the stylesheet URLs when
// only CSS changed, "reload" otherwise, "failed" with the errors.
func (l *live) built(res *api.BuildResult, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		msgs := []string{err.Error()}
		var be *BuildError
		if errors.As(err, &be) {
			msgs = be.Messages
		}
		data, _ := json.Marshal(msgs)
		l.failed = &liveEvent{"failed", string(data)}
		l.publish(*l.failed)
		return
	}
	hashes := map[string]string{}
	var changed []string
	for _, f := range res.OutputFiles {
		hashes[f.Path] = f.Hash
		if l.hashes != nil && l.hashes[f.Path] != f.Hash {
			changed = append(changed, f.Path)
		}
	}
	first, recovered := l.hashes == nil, l.failed != nil
	l.hashes, l.failed = hashes, nil
	switch {
	case recovered:
		l.publish(liveEvent{"reload", ""})
	case first || len(changed) == 0:
	case onlyCSS(changed):
		urls := make([]string, 0, len(changed))
		for _, p := range changed {
			if strings.HasSuffix(p, ".css") {
				urls = append(urls, l.url(p))
			}
		}
		data, _ := json.Marshal(urls)
		l.publish(liveEvent{"css", string(data)})
	default:
		l.publish(liveEvent{"reload", ""})
	}
}

func onlyCSS(paths []string) bool {
	for _, p := range paths {
		if !strings.HasSuffix(p, ".css") && !strings.HasSuffix(p, ".css.map") {
			return false
		}
	}
	return true
}

func (l *live) url(path string) string {
	rel, err := filepath.Rel(l.outDir, path)
	if err != nil {
		return path
	}
	return l.public + filepath.ToSlash(rel)
}

// publish sends e to every page; a page that is not reading is skipped.
// Called with l.mu held.
func (l *live) publish(e liveEvent) {
	for ch := range l.clients {
		select {
		case ch <- e:
		default:
		}
	}
}

// ServeHTTP is the event stream.
func (l *live) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the server's WriteTimeout would cut the stream
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	ch := make(chan liveEvent, 8)
	l.mu.Lock()
	l.clients[ch] = struct{}{}
	failed := l.failed
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.clients, ch)
		l.mu.Unlock()
	}()

	fmt.Fprintf(w, "retry: 500\nevent: hello\ndata: %s\n\n", l.boot)
	if failed != nil {
		writeEvent(w, *failed)
	}
	if rc.Flush() != nil {
		return
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-l.stop:
			return
		case e := <-ch:
			writeEvent(w, e)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
		}
		if rc.Flush() != nil {
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, e liveEvent) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data)
}

// script is the page's side of live reload.
func (l *live) script(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, liveScript)
}

func (l *live) tag() string {
	return `<script type="module" src="` + l.public + `__live.js"></script>`
}

const liveScript = `// fullstack/bundler live reload (development only)
const events = new EventSource(new URL("__live", import.meta.url));
let boot = null;
events.addEventListener("hello", (e) => {
  if (boot !== null && boot !== e.data) location.reload(); // the server restarted
  boot = e.data;
});
events.addEventListener("reload", () => location.reload());
events.addEventListener("css", (e) => {
  overlay(null);
  const changed = new Set(JSON.parse(e.data).map((u) => new URL(u, location.href).pathname));
  for (const link of document.querySelectorAll('link[rel="stylesheet"]')) {
    const url = new URL(link.href);
    if (!changed.has(url.pathname)) continue;
    url.searchParams.set("t", Date.now());
    const next = link.cloneNode();
    next.href = url.href;
    next.onload = next.onerror = () => link.remove();
    link.after(next);
  }
});
events.addEventListener("failed", (e) => overlay(JSON.parse(e.data)));

let box = null;
function overlay(messages) {
  if (box) box.remove();
  box = null;
  if (!messages) return;
  box = document.createElement("fullstack-error-overlay");
  const root = box.attachShadow({ mode: "open" });
  root.innerHTML =
    "<style>div{position:fixed;inset:0;z-index:2147483647;background:rgba(0,0,0,.85);color:#fff;" +
    "font:14px/1.5 ui-monospace,monospace;padding:32px;overflow:auto}h1{font-size:16px;color:#ff6b6b;margin:0 0 16px}" +
    "pre{white-space:pre-wrap;margin:0 0 12px}</style><div><h1>Build failed</h1></div>";
  for (const m of messages) {
    const pre = document.createElement("pre");
    pre.textContent = m;
    root.querySelector("div").append(pre);
  }
  document.documentElement.append(box);
}
`
