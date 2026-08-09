package cli

import (
	"reflect"
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

func TestLegacyExposeRequiresExplicitOptIn(t *testing.T) {
	t.Setenv("MOCKINGO_LEGACY_EXPOSE_ENABLED", "false")
	options, err := ParseExpose([]string{"--name", "demo", "--http", "8080"})
	if err != nil || options.Legacy {
		t.Fatalf("default options = %#v, %v", options, err)
	}
	options, err = ParseExpose([]string{"--name", "demo", "--http", "8080", "--legacy"})
	if err != nil || !options.Legacy {
		t.Fatalf("legacy options = %#v, %v", options, err)
	}
}
