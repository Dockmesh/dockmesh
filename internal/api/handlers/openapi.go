package handlers

import (
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/api/openapi"
)

// ServeOpenAPIJSON returns the OpenAPI 3.1 spec as JSON. Mounted
// under /api/v1/openapi.json without auth — the spec is public
// information, you need it to write a client even if the endpoints
// themselves require tokens.
func (h *Handlers) ServeOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	b, err := openapi.JSONBytes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(b)
}

// ServeOpenAPIYAML serves the raw YAML. Some tooling (spectral,
// openapi-generator) is happier with YAML than JSON.
func (h *Handlers) ServeOpenAPIYAML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(openapi.YAMLBytes())
}

// ServeSwaggerUI returns a minimal HTML page that loads the
// Swagger UI bundle from jsDelivr and points it at our JSON spec.
// Public endpoint — rendering the docs doesn't reveal any secrets
// that a determined reader couldn't find via the spec itself.
func (h *Handlers) ServeSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", swaggerUICSP)
	_, _ = w.Write([]byte(swaggerUIHTML))
}

const (
	swaggerUIBundleURL = "https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"
	swaggerUICSSURL    = "https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css"
)

var swaggerUICSP = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self' " + swaggerUIBundleURL + " " + middleware.ScriptHash(swaggerUIInitScript),
	"style-src 'self' 'unsafe-inline' " + swaggerUICSSURL,
	"img-src 'self' data: https:",
	"connect-src 'self'",
	"object-src 'none'",
	"base-uri 'self'",
	"frame-ancestors 'none'",
}, "; ")

const swaggerUIInitScript = `
      window.ui = SwaggerUIBundle({
        url: "/api/v1/openapi.json",
        dom_id: "#swagger-ui",
        deepLinking: true,
        presets: [SwaggerUIBundle.presets.apis],
        layout: "BaseLayout",
        tryItOutEnabled: true
      });
    `

// swaggerUIHTML embeds the Swagger UI loader. Kept verbatim in-source
// so no extra static-asset plumbing is needed — the HTML pulls the
// actual UI bundle from a pinned CDN URL. If CDN availability ever
// matters, vendor the bundle under web/static/ and swap the src.
const swaggerUIHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width,initial-scale=1">
    <title>Dockmesh API — Swagger UI</title>
    <link rel="stylesheet" href="` + swaggerUICSSURL + `" integrity="sha384-wxLW6kwyHktdDGr6Pv1zgm/VGJh99lfUbzSn6HNHBENZlCN7W602k9VkGdxuFvPn" crossorigin="anonymous">
    <style>
      body { margin: 0; background: #0f1116; }
      #swagger-ui { max-width: 1400px; margin: 0 auto; }
    </style>
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="` + swaggerUIBundleURL + `" integrity="sha384-wmyclcVGX/WhUkdkATwhaK1X1JtiNrr2EoYJ+diV3vj4v6OC5yCeSu+yW13SYJep" crossorigin="anonymous"></script>
    <script>` + swaggerUIInitScript + `</script>
  </body>
</html>
`
