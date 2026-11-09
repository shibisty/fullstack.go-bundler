package bundler

import (
	_ "embed"

	"github.com/evanw/esbuild/pkg/api"
)

// templatesScript renders view.Engine's template pack in the browser; the
// navigation script imports it (a chunk of its own) once the pack is there.
//
//go:embed client/templates.js
var templatesScript string

const templatesNS = "fullstack-templates"

// NavigateEntry is the manifest key of the page navigation script
// (Options.Navigate): links and forms load the next page with fetch and
// swap it in, without reloading scripts, styles and islands' runtime.
const NavigateEntry = "@navigate"

const navigateNS = "fullstack-navigate"

func navigatePlugin(root string) api.Plugin {
	return api.Plugin{Name: navigateNS, Setup: func(pb api.PluginBuild) {
		pb.OnResolve(api.OnResolveOptions{Filter: `^(` + navigateNS + `|` + templatesNS + `)$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: a.Path, Namespace: navigateNS}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: navigateNS}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			src := navigateScript
			if a.Path == templatesNS {
				src = templatesScript
			}
			return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS, ResolveDir: root}, nil
		})
	}}
}

// navigateScript is the browser side of Options.Navigate.
//
//   - A click on a link to this site, or a form submitted to it, fetches the
//     page; its <body> and <title> (and the <head> tags other than scripts
//     and styles) replace the current ones, the URL goes into history.
//   - When the scripts or stylesheets of the new page differ (a deploy), the
//     page is loaded the ordinary way, as on any error.
//   - Back and forward restore the page from a short cache with its scroll
//     position; a link hovered for a moment is fetched in advance.
//   - Islands are unmounted before the swap and mounted after it;
//     "fullstack:load" is dispatched on document after the first load and
//     every swap, "fullstack:visit" (cancelable) before a visit.
//   - data-reload on a link or form (or an ancestor) opts out; so do links
//     with target, download or to another origin, and modified clicks.
//   - With a template pack (fullstack/view), pages come as data and are
//     rendered here (client/templates.js); see Options.Navigate.
//
//go:embed client/navigate.js
var navigateScript string
