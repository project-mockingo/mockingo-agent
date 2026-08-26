package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/project-mockingo/mockingo-agent/internal/dependencycapture"
)

func TestDependencyRuntimePrintsLoopbackProxyWithoutWarning(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8899", "[::1]:8899"} {
		var output bytes.Buffer
		var warnings bytes.Buffer
		runtime := &dependencyRuntime{
			proxyAddress: address, certificatePath: "ca.crt",
			behaviors: dependencycapture.NewBehaviorStore(),
		}
		runtime.printStarted(&output, &warnings, "integration")
		if warnings.Len() != 0 {
			t.Fatalf("loopback %s produced warning: %s", address, warnings.String())
		}
		if !strings.Contains(output.String(), "Proxy          http://"+address) {
			t.Fatalf("loopback proxy URL missing: %s", output.String())
		}
	}
}

func TestDependencyRuntimePrintsDockerAddressesAndSecurityWarning(t *testing.T) {
	var output bytes.Buffer
	var warnings bytes.Buffer
	runtime := &dependencyRuntime{
		proxyAddress: "0.0.0.0:9000", certificatePath: "ca.crt",
		behaviors: dependencycapture.NewBehaviorStore(),
	}
	runtime.printStarted(&output, &warnings, "integration")
	text := output.String()
	for _, expected := range []string{
		"Listening      0.0.0.0:9000",
		"Host apps      http://127.0.0.1:9000",
		"Docker Desktop http://host.docker.internal:9000",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in output: %s", expected, text)
		}
	}
	if strings.Contains(text, "http://0.0.0.0:9000") {
		t.Fatalf("wildcard bind was presented as a client URL: %s", text)
	}
	if !strings.Contains(warnings.String(), "WARNING: Dependency proxy is listening outside localhost.") ||
		!strings.Contains(warnings.String(), "trusted development networks") {
		t.Fatalf("security warning missing: %s", warnings.String())
	}
}

func TestDependencyRuntimePrintsIPv6AndConcreteBindAddresses(t *testing.T) {
	for _, test := range []struct {
		address string
		want    string
	}{
		{address: "[::]:8899", want: "Host apps      http://[::1]:8899"},
		{address: "192.168.1.25:8899", want: "Client proxy   http://192.168.1.25:8899"},
	} {
		var output bytes.Buffer
		var warnings bytes.Buffer
		runtime := &dependencyRuntime{
			proxyAddress: test.address, certificatePath: "ca.crt",
			behaviors: dependencycapture.NewBehaviorStore(),
		}
		runtime.printStarted(&output, &warnings, "integration")
		if !strings.Contains(output.String(), test.want) || warnings.Len() == 0 {
			t.Fatalf("address %s output=%q warning=%q", test.address, output.String(), warnings.String())
		}
	}
}

func TestProxyBindLoopbackClassification(t *testing.T) {
	for bind, want := range map[string]bool{
		"127.0.0.1":    true,
		"::1":          true,
		"0.0.0.0":      false,
		"::":           false,
		"192.168.1.25": false,
	} {
		if got := proxyBindIsLoopback(bind); got != want {
			t.Fatalf("proxyBindIsLoopback(%q) = %v, want %v", bind, got, want)
		}
	}
}
