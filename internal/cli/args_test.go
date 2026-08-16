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
