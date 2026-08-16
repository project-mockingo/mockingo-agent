package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	mockengine "github.com/project-mockingo/mockingo-agent/internal/mock"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

const (
	MaxDocumentSize   = 10 << 20
	MaxRoutes         = 10_000
	MaxExampleDepth   = 12
	MaxStaticBodySize = tunnelprotocol.MaxBodySize
)

type WarningFunc func(string)

func Load(ctx context.Context, path string, warning WarningFunc) ([]mockengine.MockDefinition, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve OpenAPI document: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("open OpenAPI document: %w", err)
	}
	if info.IsDir() {
		return nil, errors.New("OpenAPI source must be a YAML or JSON file")
	}
	if info.Size() > MaxDocumentSize {
		return nil, fmt.Errorf("OpenAPI document exceeds maximum size of %d bytes", MaxDocumentSize)
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, fmt.Errorf("resolve OpenAPI document directory: %w", err)
	}
	loader := openapi3.NewLoader()
	loader.Context = ctx
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = localReader(root)
	document, err := loader.LoadFromFile(absolute)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	if err := document.Validate(ctx); err != nil {
		return nil, fmt.Errorf("validate OpenAPI document: %w", err)
	}
	definitions, err := convertDocument(document, filepath.Base(absolute), warning)
	if err != nil {
		return nil, err
	}
	if len(definitions) == 0 {
		return nil, errors.New("OpenAPI document contains no supported HTTP operations")
	}
	return definitions, nil
}

func localReader(root string) openapi3.ReadFromURIFunc {
	return func(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
		if location.Scheme != "" && location.Scheme != "file" {
			return nil, fmt.Errorf("remote OpenAPI reference %q is not allowed", location.String())
		}
		if location.Host != "" {
			return nil, fmt.Errorf("remote OpenAPI reference %q is not allowed", location.String())
		}
		candidate := filepath.Clean(filepath.FromSlash(location.Path))
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return nil, fmt.Errorf("OpenAPI reference %q resolves outside the document directory", location.String())
		}
		return readLimited(resolved, MaxDocumentSize)
	}
}

func convertDocument(document *openapi3.T, source string, warning WarningFunc) ([]mockengine.MockDefinition, error) {
	paths := document.Paths.Keys()
	sort.Strings(paths)
	methods := []string{http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut}
	definitions := make([]mockengine.MockDefinition, 0)
	for _, path := range paths {
		item := document.Paths.Value(path)
		operations := item.Operations()
		for _, method := range methods {
			operation := operations[method]
			if operation == nil {
				continue
			}
			if len(definitions) >= MaxRoutes {
				return nil, fmt.Errorf("OpenAPI document generates more than %d mock routes", MaxRoutes)
			}
			definition, err := convertOperation(path, method, operation, source, len(definitions), warning)
			if err != nil {
				return nil, fmt.Errorf("compile %s %s: %w", method, path, err)
			}
			definitions = append(definitions, definition)
		}
	}
	return definitions, nil
}

func convertOperation(path, method string, operation *openapi3.Operation, source string, order int, warning WarningFunc) (mockengine.MockDefinition, error) {
	status, responseRef, err := selectResponse(operation.Responses)
	if err != nil {
		return mockengine.MockDefinition{}, err
	}
	if responseRef == nil || responseRef.Value == nil {
		return mockengine.MockDefinition{}, errors.New("selected response is unresolved")
	}
	headers, err := responseHeaders(responseRef.Value.Headers, warning)
	if err != nil {
		return mockengine.MockDefinition{}, err
	}
	contentType, media := selectMediaType(responseRef.Value.Content)
	body, err := renderMedia(contentType, media, warning)
	if err != nil {
		return mockengine.MockDefinition{}, err
	}
	if len(body) > MaxStaticBodySize {
		return mockengine.MockDefinition{}, fmt.Errorf("generated response body exceeds the %d byte tunnel limit", MaxStaticBodySize)
	}
	if contentType != "" && len(body) > 0 {
		headers.Set("Content-Type", contentType)
	}
	id := operation.OperationID
	if id == "" {
		id = fmt.Sprintf("openapi-%d", order+1)
	}
	return mockengine.MockDefinition{
		ID: id, Name: operation.Summary, Priority: mockengine.DefaultPriority,
		Request:  mockengine.RequestMatcher{Method: mockengine.HTTPMethod(method), Path: mockengine.PathMatcher{Type: mockengine.PathTemplate, Value: path}},
		Response: mockengine.ResponseDefinition{Status: status, Headers: headers, Body: body},
		Source:   mockengine.SourceMetadata{Format: "openapi", File: source},
	}, nil
}

func responseHeaders(values openapi3.Headers, warning WarningFunc) (http.Header, error) {
	result := make(http.Header)
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		reference := values[name]
		if reference == nil || reference.Value == nil {
			continue
		}
		header := &reference.Value.Parameter
		var value any
		found := false
		if header.Example != nil {
			value, found = header.Example, true
		} else if len(header.Examples) > 0 {
			keys := make([]string, 0, len(header.Examples))
			for key := range header.Examples {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			example := header.Examples[keys[0]]
			if example != nil && example.Value != nil {
				if example.Value.ExternalValue != "" {
					return nil, fmt.Errorf("response header %s uses unsupported externalValue", name)
				}
				value, found = example.Value.Value, true
			}
		} else if header.Schema != nil && header.Schema.Value != nil {
			value, found = generate(header.Schema, 0, make(map[*openapi3.Schema]bool), warning), true
		}
		if !found || value == nil {
			continue
		}
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				result.Add(name, fmt.Sprint(item))
			}
		case []string:
			for _, item := range typed {
				result.Add(name, item)
			}
		case string:
			result.Set(name, typed)
		default:
			if encoded, err := json.Marshal(typed); err == nil && (strings.HasPrefix(string(encoded), "{") || strings.HasPrefix(string(encoded), "[")) {
				result.Set(name, string(encoded))
			} else {
				result.Set(name, fmt.Sprint(typed))
			}
		}
	}
	return result, nil
}

func selectResponse(responses *openapi3.Responses) (int, *openapi3.ResponseRef, error) {
	if responses == nil || responses.Len() == 0 {
		return 0, nil, errors.New("operation has no responses")
	}
	values := responses.Map()
	if value := values["200"]; value != nil {
		return http.StatusOK, value, nil
	}
	codes := make([]int, 0)
	for key := range values {
		code, err := strconv.Atoi(key)
		if err == nil && code >= 100 && code <= 599 {
			codes = append(codes, code)
		}
	}
	sort.Ints(codes)
	for _, code := range codes {
		if code >= 200 && code <= 299 {
			return code, values[strconv.Itoa(code)], nil
		}
	}
	if value := values["default"]; value != nil {
		return http.StatusOK, value, nil
	}
	if len(codes) > 0 {
		return codes[0], values[strconv.Itoa(codes[0])], nil
	}
	return 0, nil, errors.New("operation has no selectable explicit or default response")
}

func selectMediaType(content openapi3.Content) (string, *openapi3.MediaType) {
	if len(content) == 0 {
		return "", nil
	}
	if value := content["application/json"]; value != nil {
		return "application/json", value
	}
	keys := make([]string, 0, len(content))
	for key := range content {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		base := strings.ToLower(strings.TrimSpace(strings.SplitN(key, ";", 2)[0]))
		if strings.HasPrefix(base, "application/") && strings.HasSuffix(base, "+json") {
			return key, content[key]
		}
	}
	return keys[0], content[keys[0]]
}

func renderMedia(contentType string, media *openapi3.MediaType, warning WarningFunc) ([]byte, error) {
	if media == nil {
		return nil, nil
	}
	value, found, err := exampleValue(media, warning)
	if err != nil || !found {
		return nil, err
	}
	if isJSONMedia(contentType) {
		body, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode response example: %w", err)
		}
		return append(body, '\n'), nil
	}
	switch typed := value.(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return append([]byte(nil), typed...), nil
	default:
		return json.Marshal(value)
	}
}

func exampleValue(media *openapi3.MediaType, warning WarningFunc) (any, bool, error) {
	if media.Example != nil {
		return media.Example, true, nil
	}
	if len(media.Examples) > 0 {
		keys := make([]string, 0, len(media.Examples))
		for key := range media.Examples {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		selected := media.Examples[keys[0]]
		if selected == nil || selected.Value == nil {
			return nil, false, nil
		}
		if selected.Value.ExternalValue != "" {
			return nil, false, errors.New("externalValue examples are not supported")
		}
		return selected.Value.Value, true, nil
	}
	if media.Schema == nil || media.Schema.Value == nil {
		return nil, false, nil
	}
	return generate(media.Schema, 0, make(map[*openapi3.Schema]bool), warning), true, nil
}

func generate(reference *openapi3.SchemaRef, depth int, visiting map[*openapi3.Schema]bool, warning WarningFunc) any {
	if reference == nil || reference.Value == nil {
		return nil
	}
	schema := reference.Value
	if schema.Example != nil {
		return schema.Example
	}
	if schema.Default != nil {
		return schema.Default
	}
	if len(schema.Enum) > 0 {
		return schema.Enum[0]
	}
	if depth >= MaxExampleDepth || visiting[schema] {
		if warning != nil {
			warning("recursive or deeply nested OpenAPI schema was truncated while generating an example")
		}
		if schema.Type != nil && schema.Type.Is("array") {
			return []any{}
		}
		return map[string]any{}
	}
	visiting[schema] = true
	defer delete(visiting, schema)
	if len(schema.OneOf) > 0 {
		return generate(schema.OneOf[0], depth+1, visiting, warning)
	}
	if len(schema.AnyOf) > 0 {
		return generate(schema.AnyOf[0], depth+1, visiting, warning)
	}
	switch {
	case schema.Type != nil && schema.Type.Is("string"):
		return "string"
	case schema.Type != nil && schema.Type.Is("integer"):
		return 0
	case schema.Type != nil && schema.Type.Is("number"):
		return 0
	case schema.Type != nil && schema.Type.Is("boolean"):
		return false
	case schema.Type != nil && schema.Type.Is("array") || schema.Items != nil:
		if schema.Items == nil {
			return []any{}
		}
		return []any{generate(schema.Items, depth+1, visiting, warning)}
	case schema.Type != nil && schema.Type.Is("object") || len(schema.Properties) > 0:
		result := make(map[string]any, len(schema.Properties))
		keys := make([]string, 0, len(schema.Properties))
		for key := range schema.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			result[key] = generate(schema.Properties[key], depth+1, visiting, warning)
		}
		return result
	default:
		return nil
	}
}

func isJSONMedia(value string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	return base == "application/json" || strings.HasSuffix(base, "+json")
}

func readLimited(path string, maximum int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximum {
		return nil, fmt.Errorf("OpenAPI file exceeds maximum size of %d bytes", maximum)
	}
	return data, nil
}
