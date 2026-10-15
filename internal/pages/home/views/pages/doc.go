// Package pages is the presentation model of the home page's full document.
// It translates the home read model (application/results) and composed
// island regions into complete hypermedia documents (templ + htmx). Pages
// never run remote calls and never import catalog code — they receive the
// home read model and render.
package pages
