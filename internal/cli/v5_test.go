package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/project-mockingo/mockingo-agent/internal/apiclient"
)

func TestVirtualEndpointTunnelErrorIsActionable(t *testing.T) {
	err := mapTunnelSessionError("crm", &apiclient.APIError{Problem: apiclient.Problem{Status: 409, Code: "endpoint_virtual", Message: "rejected"}})
	if err == nil || !strings.Contains(err.Error(), `Endpoint "crm" is configured as Virtual.`) || !strings.Contains(err.Error(), "Switch it to Local origin") {
		t.Fatalf("error = %v", err)
	}
	if apiclient.IsRetryable(errors.Unwrap(err)) {
		t.Fatal("virtual endpoint error must not be retryable")
	}
}
