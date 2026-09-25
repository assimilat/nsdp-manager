package server

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openAPISpec []byte

// llmsGuide is served at /llms.txt: a short, plain-text orientation for a
// language model that will configure a switch through this API.
const llmsGuide = `# ProSAFE Plus NSDP API — guide for LLM-based configuration

This server exposes an OpenAPI 3.1 document at /openapi.json describing a full
tool set for configuring NETGEAR ProSAFE Plus switches. It is meant to be driven
by a language model or a generated client. Every operation has a stable
operationId you can use as a tool name.

Workflow:
1. POST /api/discover            -> list switches (each has a "mac" and "ip").
2. POST /api/switch/{mac}/login  -> body {"password": "..."} (default "password").
3. GET  /api/switch/{mac}/...    -> read a page (info, ports, vlan, qos, lags, ...).
4. POST /api/switch/{mac}/...    -> change it. Bodies match the schemas in the spec.

Notes:
- Port numbers are 1-based. Ports in a request are JSON arrays of integers.
- Static LAG only (no LACP): POST /api/switch/{mac}/lags with
  {"id":1,"enabled":true,"ports":[1,2]}. Configure the peer as a static bond too.
- Changing VLAN mode resets membership. Rate codes are indexed by the "rates"
  array returned from GET .../qos.
- reboot, factory-reset and firmware are immediate and destructive.
- Interactive docs: /docs. Machine spec: /openapi.json.
`

// swaggerHTML renders Swagger UI from a CDN against /openapi.json.
const swaggerHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>ProSAFE Plus API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
</head><body><div id="ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>window.onload=()=>{window.ui=SwaggerUIBundle({url:"openapi.json",dom_id:"#ui"});};</script>
</body></html>`

func (s *Server) registerDocs(mux *http.ServeMux) {
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(openAPISpec)
	})
	mux.HandleFunc("GET /llms.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(llmsGuide))
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(swaggerHTML))
	})
}
