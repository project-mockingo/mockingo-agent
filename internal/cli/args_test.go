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
	if !strings.Contains(output.String(), "--dependency-proxy=false") || !strings.Contains(output.String(), "--proxy-bind") || !strings.Contains(output.String(), "--proxy-port") {
		t.Fatalf("dependency proxy defaults are missing from help: %s", output.String())
	}
}

func TestStandaloneCaptureCommandIsRemoved(t *testing.T) {
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output}
	if code := app.Run(context.Background(), []string{"capture", "--name", "demo"}); code == 0 {
		t.Fatalf("removed capture command succeeded: %s", output.String())
	}
	if !strings.Contains(output.String(), `unknown command "capture"`) {
		t.Fatalf("unexpected removed-command error: %s", output.String())
	}
}

func TestTCPExposeIngressSelection(t *testing.T) {
	t.Parallel()
	options, err := ParseExpose([]string{"--name", "broker", "--tcp", "61616"})
	if err != nil {
		t.Fatal(err)
	}
	if options.Protocol() != "tcp" || options.LocalPort() != 61616 || options.HTTPPort != 0 {
		t.Fatalf("TCP options = %#v", options)
	}
	for _, args := range [][]string{
		{"--name", "broker"},
		{"--name", "broker", "--http", "8080", "--tcp", "61616"},
	} {
		if _, parseErr := ParseExpose(args); parseErr == nil {
			t.Fatalf("invalid ingress selection accepted: %v", args)
		}
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
	if !options.DependencyProxy || options.ProxyBind != "127.0.0.1" || options.ProxyPort != 8899 {
		t.Fatalf("dependency defaults = enabled:%v bind:%s port:%d", options.DependencyProxy, options.ProxyBind, options.ProxyPort)
	}
}

func TestParseExposeDependencyProxyOptionsAndOptOut(t *testing.T) {
	t.Parallel()
	options, err := ParseExpose([]string{
		"--name", "demo", "--http", "8080", "--proxy-bind", "0.0.0.0", "--proxy-port", "9000",
		"--passthrough-host", "Auth.Company.com", "--dependency-proxy=false",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.DependencyProxy || options.ProxyBind != "0.0.0.0" || options.ProxyPort != 9000 || !reflect.DeepEqual(options.PassthroughHosts, []string{"auth.company.com"}) {
		t.Fatalf("options = %#v", options)
	}
	if _, err := ParseExpose([]string{"--name", "demo", "--http", "8080", "--proxy-port", "8080"}); err == nil {
		t.Fatal("enabled dependency proxy accepted the application port")
	}
	if _, err := ParseExpose([]string{"--name", "demo", "--http", "8080", "--passthrough-host", "*.example.com"}); err == nil {
		t.Fatal("invalid expose passthrough host was accepted")
	}
}

func TestParseExposeProxyBindValidation(t *testing.T) {
	t.Parallel()
	for _, bind := range []string{"127.0.0.1", "0.0.0.0", "192.168.1.25", "::1", "::"} {
		options, err := ParseExpose([]string{"--name", "demo", "--http", "8080", "--proxy-bind", bind})
		if err != nil {
			t.Fatalf("bind %q rejected: %v", bind, err)
		}
		if options.ProxyBind == "" {
			t.Fatalf("bind %q normalized to empty", bind)
		}
	}
	for _, bind := range []string{"", "999.999.999.999", "127.0.0.1:8899", "not an address"} {
		if _, err := ParseExpose([]string{"--name", "demo", "--http", "8080", "--proxy-bind", bind}); err == nil {
			t.Fatalf("invalid bind %q accepted", bind)
		}
	}
}

func TestRemovedExposeOptionsAreRejected(t *testing.T) {
	for _, option := range []string{"--legacy", "--token", "--gateway-token"} {
		if _, err := ParseExpose([]string{"--name", "demo", "--http", "8080", option, "value"}); err == nil {
			t.Fatalf("removed option %s was accepted", option)
		}
	}
}
