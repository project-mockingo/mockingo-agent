package cli

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestParseEnvironment(t *testing.T) {
	t.Parallel()
	got, err := ParseEnvironment([]string{"A=one", "EMPTY=", "A=two=three"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "two=three", "EMPTY": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if _, err := ParseEnvironment([]string{"MISSING"}); err == nil {
		t.Fatal("expected malformed environment error")
	}
}

func TestExposeHelpIsOAuthOnly(t *testing.T) {
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output}
	if code := app.Run(context.Background(), []string{"expose", "--help"}); code != 0 {
		t.Fatalf("help exit code = %d: %s", code, output.String())
	}
	if strings.Contains(output.String(), "--legacy") || strings.Contains(output.String(), "--token") {
		t.Fatalf("removed option appears in help: %s", output.String())
	}
	if !strings.Contains(output.String(), "--wiremock") || !strings.Contains(output.String(), "--openapi") {
		t.Fatalf("hybrid source options missing from help: %s", output.String())
	}
}

func TestParseExposeMockSourcesAreOptionalAndMutuallyExclusive(t *testing.T) {
	t.Parallel()
	pure, err := ParseExpose([]string{"--name", "demo", "--http", "8080"})
	if err != nil || pure.WireMock != "" || pure.OpenAPI != "" {
		t.Fatalf("pure expose options = %#v, %v", pure, err)
	}
	wireMock, err := ParseExpose([]string{"--name", "demo", "--http", "8080", "--wiremock", "wiremock"})
	if err != nil || wireMock.WireMock != "wiremock" {
		t.Fatalf("WireMock options = %#v, %v", wireMock, err)
	}
	openAPI, err := ParseExpose([]string{"--name", "demo", "--http", "8080", "--openapi", "partial.yaml"})
	if err != nil || openAPI.OpenAPI != "partial.yaml" {
		t.Fatalf("OpenAPI options = %#v, %v", openAPI, err)
	}
	if _, err := ParseExpose([]string{"--name", "demo", "--http", "8080", "--wiremock", "wiremock", "--openapi", "partial.yaml"}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("two hybrid sources error = %v", err)
	}
}

func TestParseExposePreservesCommandArguments(t *testing.T) {
	t.Parallel()
	args := []string{"--name", "demo", "--http", "8080", "--env", "A=B", "--", "java", "-Dmessage=hello world", "-jar", "app.jar"}
	options, err := ParseExpose(args)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"java", "-Dmessage=hello world", "-jar", "app.jar"}
	if !reflect.DeepEqual(options.Command, want) {
		t.Fatalf("command = %#v, want %#v", options.Command, want)
	}
}

func TestRemovedExposeOptionsAreRejected(t *testing.T) {
	for _, option := range []string{"--legacy", "--token", "--gateway-token"} {
		if _, err := ParseExpose([]string{"--name", "demo", "--http", "8080", option, "value"}); err == nil {
			t.Fatalf("removed option %s was accepted", option)
		}
	}
}

func TestParseMockRequiresExactlyOneSource(t *testing.T) {
	t.Parallel()
	if _, err := ParseMock([]string{"--name", "weather"}); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := ParseMock([]string{"--name", "weather", "--wiremock", "wiremock", "--openapi", "openapi.yaml"}); err == nil {
		t.Fatal("two sources accepted")
	}
	options, err := ParseMock([]string{"--name", "weather", "--wiremock", "wiremock"})
	if err != nil || options.WireMock != "wiremock" || options.Name != "weather" {
		t.Fatalf("options = %#v, %v", options, err)
	}
}
