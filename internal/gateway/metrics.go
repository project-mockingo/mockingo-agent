package gateway

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

type metrics struct {
	httpRequests       atomic.Uint64
	httpDurationMicros atomic.Uint64
	reconnects         atomic.Uint64
	registrationErrors atomic.Uint64
}

func (m *metrics) observeRequest(duration time.Duration) {
	m.httpRequests.Add(1)
	m.httpDurationMicros.Add(uint64(duration.Microseconds()))
}

func (m *metrics) write(w io.Writer, active, registered int) {
	requests := m.httpRequests.Load()
	duration := float64(m.httpDurationMicros.Load()) / 1_000_000
	fmt.Fprintf(w, "# TYPE active_tunnels gauge\nactive_tunnels %d\n", active)
	fmt.Fprintf(w, "# TYPE registered_endpoints gauge\nregistered_endpoints %d\n", registered)
	fmt.Fprintf(w, "# TYPE http_requests_total counter\nhttp_requests_total %d\n", requests)
	fmt.Fprintf(w, "# TYPE http_request_duration_seconds summary\nhttp_request_duration_seconds_sum %g\nhttp_request_duration_seconds_count %d\n", duration, requests)
	fmt.Fprintf(w, "# TYPE tunnel_reconnects_total counter\ntunnel_reconnects_total %d\n", m.reconnects.Load())
	fmt.Fprintf(w, "# TYPE tunnel_registration_errors_total counter\ntunnel_registration_errors_total %d\n", m.registrationErrors.Load())
}
