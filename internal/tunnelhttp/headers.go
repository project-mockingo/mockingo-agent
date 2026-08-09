// Package tunnelhttp contains CLI-local HTTP forwarding policy. It is not
// part of the wire contract even though the gateway has an equivalent helper.
package tunnelhttp

import (
	"net/http"
	"strings"
)

var hopByHop = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

// FilterHeaders returns a copy without RFC hop-by-hop headers, including
// headers nominated by the Connection header.
func FilterHeaders(src http.Header) http.Header {
	blocked := make(map[string]struct{}, len(hopByHop))
	for key := range hopByHop {
		blocked[key] = struct{}{}
	}
	for _, value := range src.Values("Connection") {
		for _, key := range strings.Split(value, ",") {
			blocked[strings.ToLower(strings.TrimSpace(key))] = struct{}{}
		}
	}
	out := make(http.Header)
	for key, values := range src {
		if _, found := blocked[strings.ToLower(key)]; found {
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	return out
}
