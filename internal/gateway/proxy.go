package gateway

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

func ParseTrustedProxyCIDRs(value string) ([]*net.IPNet, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	networks := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		_, network, err := net.ParseCIDR(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", part, err)
		}
		networks = append(networks, network)
	}
	return networks, nil
}

func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

func isTrustedProxy(remoteAddr string, networks []*net.IPNet) bool {
	ip := remoteIP(remoteAddr)
	if ip == nil {
		return false
	}
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func forwardedHeaders(r *http.Request, publicScheme string, trusted []*net.IPNet) (clientIP, host, scheme string) {
	clientIP = remoteIP(r.RemoteAddr).String()
	host = r.Host
	scheme = publicScheme
	if !isTrustedProxy(r.RemoteAddr, trusted) {
		return
	}
	// Use only the last proxy-supplied address. This prevents a caller-controlled
	// leftmost X-Forwarded-For value from being propagated by Mockingo.
	values := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if candidate := strings.TrimSpace(values[len(values)-1]); net.ParseIP(candidate) != nil {
		clientIP = candidate
	}
	if value := r.Header.Get("X-Forwarded-Proto"); value == "http" || value == "https" {
		scheme = value
	}
	return
}
