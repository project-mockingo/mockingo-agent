package gateway

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/mockingo/mockingo-cli/internal/naming"
)

func ParseEndpointHost(rawHost, baseDomain string) (string, string, error) {
	if rawHost == "" || rawHost != strings.TrimSpace(rawHost) || strings.ContainsAny(rawHost, ",/@\\") {
		return "", "", fmt.Errorf("malformed Host header")
	}
	for _, char := range rawHost {
		if char > 127 || char <= 32 {
			return "", "", fmt.Errorf("Host must be an ASCII hostname")
		}
	}
	host := rawHost
	if strings.Contains(host, ":") {
		parsedHost, port, err := net.SplitHostPort(host)
		if err != nil || parsedHost == "" {
			return "", "", fmt.Errorf("malformed host port")
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", "", fmt.Errorf("invalid host port")
		}
		host = parsedHost
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	base := strings.ToLower(strings.TrimSuffix(baseDomain, "."))
	if net.ParseIP(host) != nil || host == base || !strings.HasSuffix(host, "."+base) {
		return "", "", fmt.Errorf("host is outside the endpoint domain")
	}
	name := strings.TrimSuffix(host, "."+base)
	if strings.Contains(name, ".") || naming.Validate(name) != nil {
		return "", "", fmt.Errorf("host is not a valid endpoint hostname")
	}
	return name, host, nil
}
