package openapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadYAMLOperationsTemplatesSelectionAndExamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openapi.yaml")
	write(t, path, `openapi: 3.0.3
info: {title: Test, version: 1.0.0}
paths:
  /users/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: string}}
    get:
      operationId: getUser
      responses:
        '201':
          description: created
          content:
            application/json:
              schema:
                type: object
                properties:
                  name: {type: string}
                  active: {type: boolean}
        '200':
          description: ok
          headers:
            X-Rate-Limit: {schema: {type: integer}, example: 5}
          content:
            application/json:
              example: {name: Prague}
    post:
      responses:
        '202': {description: accepted}
        '201': {description: created}
  /fallback:
    put:
      responses:
        default: {description: fallback}
  /error:
    patch:
      responses:
        '500': {description: failed}
        '404': {description: absent}
  /named:
    delete:
      responses:
        '200':
          description: ok
          content:
            application/problem+json:
              examples:
                z: {value: {code: z}}
                a: {value: {code: a}}
  /empty:
    head:
      responses:
        '204': {description: empty}
  /options:
    options:
      responses:
        '200': {description: ok}
`)
	definitions, err := Load(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 7 {
		t.Fatalf("route count = %d", len(definitions))
	}
	byID := make(map[string]struct {
		status int
		body   string
		ctype  string
	})
	for _, definition := range definitions {
		byID[definition.ID] = struct {
			status int
			body   string
			ctype  string
		}{definition.Response.Status, string(definition.Response.Body), definition.Response.Headers.Get("Content-Type")}
	}
	if got := byID["getUser"]; got.status != 200 || !strings.Contains(got.body, "Prague") || got.ctype != "application/json" {
		t.Fatalf("200/example selection = %#v", got)
	}
	for _, definition := range definitions {
		if definition.ID == "getUser" && definition.Response.Headers.Get("X-Rate-Limit") != "5" {
			t.Fatalf("OpenAPI response header = %#v", definition.Response.Headers)
		}
	}
	for _, definition := range definitions {
		switch definition.Request.Path.Value {
		case "/users/{id}":
			if string(definition.Request.Method) == "POST" && definition.Response.Status != 201 {
				t.Fatalf("lowest 2xx = %d", definition.Response.Status)
			}
		case "/fallback":
			if definition.Response.Status != 200 {
				t.Fatalf("default status = %d", definition.Response.Status)
			}
		case "/error":
			if definition.Response.Status != 404 {
				t.Fatalf("lowest explicit status = %d", definition.Response.Status)
			}
		case "/named":
			if !strings.Contains(string(definition.Response.Body), `"a"`) || definition.Response.Headers.Get("Content-Type") != "application/problem+json" {
				t.Fatalf("named example = %s %#v", definition.Response.Body, definition.Response.Headers)
			}
		}
	}
}

func TestJSONLocalReferenceGenerationAndRecursion(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "schemas", "user.yaml"), `type: object
properties:
  id: {type: integer}
  score: {type: number, default: 1.5}
  role: {type: string, enum: [admin, user]}
  tags: {type: array, items: {type: string}}
`)
	document := filepath.Join(root, "openapi.json")
	write(t, document, `{"openapi":"3.0.3","info":{"title":"Test","version":"1"},"paths":{"/user":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"./schemas/user.yaml"}}}}}}}}}`)
	definitions, err := Load(context.Background(), document, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := string(definitions[0].Response.Body)
	for _, value := range []string{`"id": 0`, `"score": 1.5`, `"role": "admin"`, `"string"`} {
		if !strings.Contains(body, value) {
			t.Fatalf("generated body %q lacks %q", body, value)
		}
	}
	recursive := filepath.Join(root, "recursive.yaml")
	write(t, recursive, `openapi: 3.0.3
info: {title: Recursive, version: '1'}
paths:
  /node:
    get:
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Node'}
components:
  schemas:
    Node:
      type: object
      properties:
        value: {type: string}
        child: {$ref: '#/components/schemas/Node'}
`)
	var warnings []string
	if _, err := Load(context.Background(), recursive, func(value string) { warnings = append(warnings, value) }); err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Fatal("recursive schema did not emit warning")
	}
}

func TestRemoteReferenceAndMalformedDocumentsFail(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.yaml")
	write(t, remote, `openapi: 3.0.3
info: {title: Test, version: '1'}
paths:
  /x:
    get:
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: 'https://example.com/schema.yaml'}
`)
	if _, err := Load(context.Background(), remote, nil); err == nil || !strings.Contains(err.Error(), "remote OpenAPI reference") {
		t.Fatalf("remote ref error = %v", err)
	}
	malformed := filepath.Join(root, "invalid.yaml")
	write(t, malformed, "openapi: [")
	if _, err := Load(context.Background(), malformed, nil); err == nil {
		t.Fatal("malformed document loaded")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
