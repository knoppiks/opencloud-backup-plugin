package api

import "net/http"

// responseHeaders are set on every response this service sends, errors and
// probes included (#90).
//
//   - Cache-Control: no-store keeps responses out of the browser's cache and
//     any shared cache in between. Some carry a Space's recovery envelope,
//     target configuration or grants; none is worth reusing.
//   - X-Content-Type-Options: nosniff stops a browser from reading a JSON
//     answer as anything but the type it was sent as.
var responseHeaders = map[string]string{
	"Cache-Control":          "no-store",
	"X-Content-Type-Options": "nosniff",
}

// WithResponseHeaders sets responseHeaders before next runs, so they are on
// every answer next or anything wrapped in it writes, a recovered panic's
// included. Exported for the wiring in front of the API (a base-path mount),
// whose own 404 is a response of this service too.
func WithResponseHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for name, value := range responseHeaders {
			h.Set(name, value)
		}
		next.ServeHTTP(w, r)
	})
}
