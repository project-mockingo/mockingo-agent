package gateway

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

type Metrics struct {
	httpRequests               atomic.Uint64
	httpDurationMicros         atomic.Uint64
	reconnects                 atomic.Uint64
	registrationErrors         atomic.Uint64
	connectionsTicket          atomic.Uint64
	connectionsLegacy          atomic.Uint64
	ticketValidations          atomic.Uint64
	ticketFailures             atomic.Uint64
	ticketReplay               atomic.Uint64
	jwksRefresh                atomic.Uint64
	jwksFailures               atomic.Uint64
	callbacks                  atomic.Uint64
	callbackFailures           atomic.Uint64
	internalStatusRequests     atomic.Uint64
	internalDisconnectRequests atomic.Uint64
}

func NewMetrics() *Metrics { return &Metrics{} }

func (m *Metrics) observeRequest(duration time.Duration) {
	m.httpRequests.Add(1)
	m.httpDurationMicros.Add(uint64(duration.Microseconds()))
}

func (m *Metrics) tunnelConnected(authMethod string) {
	if authMethod == string(TunnelAuthTicket) {
		m.connectionsTicket.Add(1)
	} else {
		m.connectionsLegacy.Add(1)
	}
}

func (m *Metrics) ObserveTicketValidation(result string) {
	m.ticketValidations.Add(1)
	if result != "success" {
		m.ticketFailures.Add(1)
	}
}

func (m *Metrics) ObserveTicketReplay() { m.ticketReplay.Add(1) }

func (m *Metrics) ObserveJWKSRefresh(success bool) {
	m.jwksRefresh.Add(1)
	if !success {
		m.jwksFailures.Add(1)
	}
}

func (m *Metrics) ObserveCallback(_ string, success bool) {
	m.callbacks.Add(1)
	if !success {
		m.callbackFailures.Add(1)
	}
}

func (m *Metrics) write(w io.Writer, active, registered int) {
	requests := m.httpRequests.Load()
	duration := float64(m.httpDurationMicros.Load()) / 1_000_000
	fmt.Fprintf(w, "# TYPE mockingo_gateway_active_tunnels gauge\nmockingo_gateway_active_tunnels %d\n", active)
	fmt.Fprintf(w, "# TYPE registered_endpoints gauge\nregistered_endpoints %d\n", registered)
	fmt.Fprintf(w, "# TYPE http_requests_total counter\nhttp_requests_total %d\n", requests)
	fmt.Fprintf(w, "# TYPE http_request_duration_seconds summary\nhttp_request_duration_seconds_sum %g\nhttp_request_duration_seconds_count %d\n", duration, requests)
	fmt.Fprintf(w, "# TYPE tunnel_reconnects_total counter\ntunnel_reconnects_total %d\n", m.reconnects.Load())
	fmt.Fprintf(w, "# TYPE tunnel_registration_errors_total counter\ntunnel_registration_errors_total %d\n", m.registrationErrors.Load())
	fmt.Fprintln(w, "# TYPE mockingo_gateway_tunnel_connections_total counter")
	fmt.Fprintf(w, "mockingo_gateway_tunnel_connections_total{auth_method=\"ticket\"} %d\n", m.connectionsTicket.Load())
	fmt.Fprintf(w, "mockingo_gateway_tunnel_connections_total{auth_method=\"legacy\"} %d\n", m.connectionsLegacy.Load())
	fmt.Fprintf(w, "mockingo_gateway_ticket_validation_total %d\n", m.ticketValidations.Load())
	fmt.Fprintf(w, "mockingo_gateway_ticket_validation_failures_total %d\n", m.ticketFailures.Load())
	fmt.Fprintf(w, "mockingo_gateway_ticket_replay_rejections_total %d\n", m.ticketReplay.Load())
	fmt.Fprintf(w, "mockingo_gateway_jwks_refresh_total %d\n", m.jwksRefresh.Load())
	fmt.Fprintf(w, "mockingo_gateway_jwks_refresh_failures_total %d\n", m.jwksFailures.Load())
	fmt.Fprintf(w, "mockingo_gateway_backend_callbacks_total %d\n", m.callbacks.Load())
	fmt.Fprintf(w, "mockingo_gateway_backend_callback_failures_total %d\n", m.callbackFailures.Load())
	fmt.Fprintf(w, "mockingo_gateway_internal_status_requests_total %d\n", m.internalStatusRequests.Load())
	fmt.Fprintf(w, "mockingo_gateway_internal_disconnect_requests_total %d\n", m.internalDisconnectRequests.Load())
}
