package mock

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type Engine interface {
	Match(*http.Request) (*MockDefinition, bool)
}

type compiledDefinition struct {
	definition MockDefinition
	order      int
	template   []string
}

type CompiledEngine struct {
	definitions []compiledDefinition
}

func Compile(definitions []MockDefinition) *CompiledEngine {
	compiled := make([]compiledDefinition, len(definitions))
	for i := range definitions {
		definition := cloneDefinition(definitions[i])
		compiled[i] = compiledDefinition{definition: definition, order: i}
		if definition.Request.Path.Type == PathTemplate {
			compiled[i].template = splitPath(definition.Request.Path.Value)
		}
	}
	sort.SliceStable(compiled, func(i, j int) bool {
		if compiled[i].definition.Priority != compiled[j].definition.Priority {
			return compiled[i].definition.Priority < compiled[j].definition.Priority
		}
		return compiled[i].order < compiled[j].order
	})
	return &CompiledEngine{definitions: compiled}
}

func (e *CompiledEngine) Match(request *http.Request) (*MockDefinition, bool) {
	for i := range e.definitions {
		candidate := &e.definitions[i]
		if candidate.definition.Request.Method != MethodAny && string(candidate.definition.Request.Method) != request.Method {
			continue
		}
		if !matchPath(candidate, request) {
			continue
		}
		result := cloneDefinition(candidate.definition)
		return &result, true
	}
	return nil, false
}

func matchPath(candidate *compiledDefinition, request *http.Request) bool {
	switch candidate.definition.Request.Path.Type {
	case PathExactURL:
		return request.URL.RequestURI() == candidate.definition.Request.Path.Value
	case PathExactPath:
		return request.URL.Path == candidate.definition.Request.Path.Value
	case PathTemplate:
		actual := splitPath(request.URL.Path)
		if len(actual) != len(candidate.template) {
			return false
		}
		for i := range actual {
			expected := candidate.template[i]
			if strings.HasPrefix(expected, "{") && strings.HasSuffix(expected, "}") && len(expected) > 2 {
				if actual[i] == "" {
					return false
				}
				continue
			}
			if actual[i] != expected {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func splitPath(value string) []string {
	if value == "/" {
		return []string{""}
	}
	return strings.Split(strings.TrimPrefix(value, "/"), "/")
}

func cloneDefinition(source MockDefinition) MockDefinition {
	result := source
	result.Response.Headers = source.Response.Headers.Clone()
	result.Response.Body = append([]byte(nil), source.Response.Body...)
	return result
}

type Handler struct {
	Engine Engine
	Done   <-chan struct{}
	Log    func(method, path string, status int, matched bool)
}

func (h Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	definition, found := h.Engine.Match(request)
	if !found {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusNotFound)
		if request.Method != http.MethodHead {
			_ = json.NewEncoder(writer).Encode(map[string]string{"code": "mock_not_found", "message": "No mock matched this request."})
		}
		if h.Log != nil {
			h.Log(request.Method, request.URL.RequestURI(), http.StatusNotFound, false)
		}
		return
	}
	if definition.Response.FixedDelay > 0 {
		timer := timeNewTimer(definition.Response.FixedDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-request.Context().Done():
			return
		case <-h.Done:
			return
		}
	}
	for name, values := range definition.Response.Headers {
		for _, value := range values {
			writer.Header().Add(name, value)
		}
	}
	if statusAllowsBody(definition.Response.Status) && writer.Header().Get("Content-Length") == "" {
		writer.Header().Set("Content-Length", strconv.Itoa(len(definition.Response.Body)))
	}
	writer.WriteHeader(definition.Response.Status)
	if request.Method != http.MethodHead && statusAllowsBody(definition.Response.Status) {
		_, _ = writer.Write(definition.Response.Body)
	}
	if h.Log != nil {
		h.Log(request.Method, request.URL.RequestURI(), definition.Response.Status, true)
	}
}

func statusAllowsBody(status int) bool {
	return status < 100 || status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}
