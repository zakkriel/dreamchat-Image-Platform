package http

import (
	"encoding/json"
	"fmt"
	"net/http"

	"gopkg.in/yaml.v3"

	"github.com/zakkriel/drchat-image-platform/api"
)

const swaggerUICDN = "https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14"

var openAPIJSON []byte

func init() {
	var doc any
	if err := yaml.Unmarshal(api.OpenAPIYAML, &doc); err != nil {
		panic(fmt.Errorf("embed openapi: parse yaml: %w", err))
	}
	doc = normalizeYAMLValue(doc)
	data, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Errorf("embed openapi: marshal json: %w", err))
	}
	openAPIJSON = data
}

// normalizeYAMLValue converts the map[interface{}]interface{} (or its
// successor in yaml.v3, map[string]interface{}) tree into a JSON-marshalable
// shape with map[string]interface{} everywhere.
func normalizeYAMLValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeYAMLValue(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprintf("%v", k)] = normalizeYAMLValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeYAMLValue(val)
		}
		return out
	default:
		return v
	}
}

func openAPIJSONHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPIJSON)
}

func docsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, swaggerHTML, swaggerUICDN, swaggerUICDN, swaggerUICDN)
}

const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8" />
<title>DreamChat Image Platform API</title>
<link rel="stylesheet" href="%s/swagger-ui.css" />
</head>
<body>
<div id="swagger-ui"></div>
<script src="%s/swagger-ui-bundle.js"></script>
<script src="%s/swagger-ui-standalone-preset.js"></script>
<script>
window.onload = function() {
  window.ui = SwaggerUIBundle({
    url: "/openapi.json",
    dom_id: "#swagger-ui",
    presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePreset],
    layout: "BaseLayout"
  });
};
</script>
</body>
</html>
`
