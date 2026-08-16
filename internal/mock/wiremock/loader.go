package wiremock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	mockengine "github.com/project-mockingo/mockingo-agent/internal/mock"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

const (
	MaxMappings       = 10_000
	MaxMappingFile    = 5 << 20
	MaxFixedDelay     = 30 * time.Second
	MaxStaticBodySize = tunnelprotocol.MaxBodySize
)

type mapping struct {
	ID       string
	Name     string
	Priority int
	Request  request
	Response response
	File     string
}

type request struct {
	Method          string
	URL             string
	URLPath         string
	URLPathTemplate string
}

type response struct {
	Status     int
	Headers    http.Header
	Body       []byte
	BodySource string
	Delay      time.Duration
}

func Load(path string) ([]mockengine.MockDefinition, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve WireMock source: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("open WireMock source: %w", err)
	}
	var files []string
	var filesRoot string
	if info.IsDir() {
		mappingsRoot := filepath.Join(absolute, "mappings")
		entries, readErr := os.ReadDir(mappingsRoot)
		if readErr != nil {
			return nil, fmt.Errorf("read WireMock mappings directory %s: %w", mappingsRoot, readErr)
		}
		for _, entry := range entries {
			if entry.IsDir() || ignoredFile(entry.Name()) {
				continue
			}
			if !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				return nil, fmt.Errorf("unsupported file in WireMock mappings directory: %s", entry.Name())
			}
			files = append(files, filepath.Join(mappingsRoot, entry.Name()))
		}
		filesRoot = filepath.Join(absolute, "__files")
	} else {
		if !strings.EqualFold(filepath.Ext(absolute), ".json") {
			return nil, errors.New("direct WireMock mapping must be a JSON file")
		}
		files = []string{absolute}
		directory := filepath.Dir(absolute)
		if strings.EqualFold(filepath.Base(directory), "mappings") {
			filesRoot = filepath.Join(filepath.Dir(directory), "__files")
		} else {
			filesRoot = filepath.Join(directory, "__files")
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, errors.New("WireMock source contains no mapping JSON files")
	}
	if len(files) > MaxMappings {
		return nil, fmt.Errorf("WireMock source contains %d mappings; maximum is %d", len(files), MaxMappings)
	}
	definitions := make([]mockengine.MockDefinition, 0, len(files))
	for _, file := range files {
		parsed, parseErr := parseFile(file, filesRoot)
		if parseErr != nil {
			return nil, fmt.Errorf("failed to load WireMock mapping %s: %w", filepath.Base(file), parseErr)
		}
		definitions = append(definitions, convert(parsed, len(definitions)))
	}
	return definitions, nil
}

func ignoredFile(name string) bool {
	return name == ".DS_Store" || strings.HasSuffix(name, ".swp") || strings.HasSuffix(name, "~")
}

func parseFile(path, filesRoot string) (mapping, error) {
	data, err := readLimited(path, MaxMappingFile)
	if err != nil {
		return mapping{}, err
	}
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return mapping{}, fmt.Errorf("malformed JSON: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return mapping{}, err
	}
	allowedTop := set("id", "name", "priority", "request", "response")
	if err := rejectUnsupported(raw, allowedTop, "mapping"); err != nil {
		return mapping{}, err
	}
	result := mapping{Priority: mockengine.DefaultPriority, File: filepath.Base(path)}
	if err := decodeOptional(raw, "id", &result.ID); err != nil {
		return mapping{}, err
	}
	if err := decodeOptional(raw, "name", &result.Name); err != nil {
		return mapping{}, err
	}
	if value, ok := raw["priority"]; ok {
		if err := json.Unmarshal(value, &result.Priority); err != nil || result.Priority < 1 {
			return mapping{}, errors.New("priority must be a positive integer")
		}
	}
	requestRaw, ok := raw["request"]
	if !ok {
		return mapping{}, errors.New("request is required")
	}
	if err := parseRequest(requestRaw, &result.Request); err != nil {
		return mapping{}, err
	}
	responseRaw, ok := raw["response"]
	if !ok {
		return mapping{}, errors.New("response is required")
	}
	if err := parseResponse(responseRaw, filesRoot, &result.Response); err != nil {
		return mapping{}, err
	}
	return result, nil
}

func parseRequest(data json.RawMessage, result *request) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return errors.New("request must be an object")
	}
	allowed := set("method", "url", "urlPath", "urlPathTemplate")
	if err := rejectUnsupported(raw, allowed, "request"); err != nil {
		return err
	}
	if err := decodeRequired(raw, "method", &result.Method); err != nil {
		return err
	}
	result.Method = strings.ToUpper(result.Method)
	if !validMethod(result.Method) {
		return fmt.Errorf("unsupported request.method %q", result.Method)
	}
	if err := decodeOptional(raw, "url", &result.URL); err != nil {
		return err
	}
	if err := decodeOptional(raw, "urlPath", &result.URLPath); err != nil {
		return err
	}
	if err := decodeOptional(raw, "urlPathTemplate", &result.URLPathTemplate); err != nil {
		return err
	}
	count := boolInt(result.URL != "") + boolInt(result.URLPath != "") + boolInt(result.URLPathTemplate != "")
	if count != 1 {
		return errors.New("request must contain exactly one of url, urlPath, or urlPathTemplate")
	}
	path := result.URL
	if path == "" {
		path = result.URLPath
	}
	if path == "" {
		path = result.URLPathTemplate
	}
	if !strings.HasPrefix(path, "/") {
		return errors.New("request URL/path must start with /")
	}
	return nil
}

func parseResponse(data json.RawMessage, filesRoot string, result *response) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return errors.New("response must be an object")
	}
	allowed := set("status", "headers", "body", "jsonBody", "bodyFileName", "fixedDelayMilliseconds")
	if err := rejectUnsupported(raw, allowed, "response"); err != nil {
		return err
	}
	if err := decodeRequired(raw, "status", &result.Status); err != nil {
		return err
	}
	if result.Status < 100 || result.Status > 599 {
		return errors.New("response.status must be between 100 and 599")
	}
	if value, ok := raw["headers"]; ok {
		headers, err := parseHeaders(value)
		if err != nil {
			return err
		}
		result.Headers = headers
	} else {
		result.Headers = make(http.Header)
	}
	bodyFields := 0
	if value, ok := raw["body"]; ok {
		var body string
		if err := json.Unmarshal(value, &body); err != nil {
			return errors.New("response.body must be a string")
		}
		result.Body = []byte(body)
		result.BodySource = "body"
		bodyFields++
	}
	if value, ok := raw["jsonBody"]; ok {
		if !json.Valid(value) {
			return errors.New("response.jsonBody must be valid JSON")
		}
		result.Body = append([]byte(nil), value...)
		result.BodySource = "jsonBody"
		if result.Headers.Get("Content-Type") == "" {
			result.Headers.Set("Content-Type", "application/json")
		}
		bodyFields++
	}
	if value, ok := raw["bodyFileName"]; ok {
		var name string
		if err := json.Unmarshal(value, &name); err != nil || name == "" {
			return errors.New("response.bodyFileName must be a non-empty string")
		}
		body, err := readBodyFile(filesRoot, name)
		if err != nil {
			return err
		}
		result.Body = body
		result.BodySource = "bodyFileName"
		bodyFields++
	}
	if bodyFields > 1 {
		return errors.New("response may contain only one of body, jsonBody, or bodyFileName")
	}
	if len(result.Body) > MaxStaticBodySize {
		return fmt.Errorf("response body exceeds the %d byte tunnel limit", MaxStaticBodySize)
	}
	if value, ok := raw["fixedDelayMilliseconds"]; ok {
		var milliseconds int64
		if err := json.Unmarshal(value, &milliseconds); err != nil || milliseconds < 0 {
			return errors.New("response.fixedDelayMilliseconds must be a non-negative integer")
		}
		result.Delay = time.Duration(milliseconds) * time.Millisecond
		if result.Delay > MaxFixedDelay {
			return fmt.Errorf("response.fixedDelayMilliseconds exceeds maximum %d", MaxFixedDelay.Milliseconds())
		}
	}
	return nil
}

func parseHeaders(data json.RawMessage) (http.Header, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.New("response.headers must be an object")
	}
	result := make(http.Header, len(raw))
	for name, value := range raw {
		var one string
		if json.Unmarshal(value, &one) == nil {
			result[name] = []string{one}
			continue
		}
		var many []string
		if err := json.Unmarshal(value, &many); err != nil {
			return nil, fmt.Errorf("response header %s must be a string or string array", name)
		}
		result[name] = append([]string(nil), many...)
	}
	return result, nil
}

func readBodyFile(root, name string) ([]byte, error) {
	if name == "" || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return nil, errors.New("response.bodyFileName must be a relative path under __files")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("response.bodyFileName escapes the __files directory")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve __files directory: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve __files directory: %w", err)
	}
	targetResolved, err := filepath.EvalSymlinks(filepath.Join(rootAbs, clean))
	if err != nil {
		return nil, fmt.Errorf("read response.bodyFileName %q: %w", name, err)
	}
	relative, err := filepath.Rel(rootResolved, targetResolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("response.bodyFileName resolves outside the __files directory")
	}
	data, err := readLimited(targetResolved, MaxStaticBodySize)
	if err != nil {
		return nil, fmt.Errorf("read response.bodyFileName %q: %w", name, err)
	}
	return data, nil
}

func convert(value mapping, order int) mockengine.MockDefinition {
	matcher := mockengine.PathMatcher{Type: mockengine.PathExactURL, Value: value.Request.URL}
	if value.Request.URLPath != "" {
		matcher = mockengine.PathMatcher{Type: mockengine.PathExactPath, Value: value.Request.URLPath}
	} else if value.Request.URLPathTemplate != "" {
		matcher = mockengine.PathMatcher{Type: mockengine.PathTemplate, Value: value.Request.URLPathTemplate}
	}
	id := value.ID
	if id == "" {
		id = fmt.Sprintf("wiremock-%d", order+1)
	}
	return mockengine.MockDefinition{
		ID: id, Name: value.Name, Priority: value.Priority,
		Request:  mockengine.RequestMatcher{Method: mockengine.HTTPMethod(value.Request.Method), Path: matcher},
		Response: mockengine.ResponseDefinition{Status: value.Response.Status, Headers: value.Response.Headers, Body: value.Response.Body, FixedDelay: value.Response.Delay},
		Source:   mockengine.SourceMetadata{Format: "wiremock", File: value.File},
	}
}

func rejectUnsupported(raw map[string]json.RawMessage, allowed map[string]struct{}, location string) error {
	keys := make([]string, 0)
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return fmt.Errorf("unsupported feature: %s.%s is not supported in Mockingo M1", location, keys[0])
}

func set(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func decodeRequired(raw map[string]json.RawMessage, key string, target any) error {
	value, ok := raw[key]
	if !ok {
		return fmt.Errorf("%s is required", key)
	}
	if err := json.Unmarshal(value, target); err != nil {
		return fmt.Errorf("%s has an invalid value", key)
	}
	return nil
}

func decodeOptional(raw map[string]json.RawMessage, key string, target any) error {
	value, ok := raw[key]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(value, target); err != nil {
		return fmt.Errorf("%s has an invalid value", key)
	}
	return nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("mapping JSON contains trailing data")
	}
	return nil
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
		return nil, fmt.Errorf("file exceeds maximum size of %d bytes", maximum)
	}
	return data, nil
}

func validMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions, string(mockengine.MethodAny):
		return true
	default:
		return false
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
