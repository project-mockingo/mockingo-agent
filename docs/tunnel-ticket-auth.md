# Tunnel ticket use in the CLI

The Spring Boot control plane returns a short-lived, one-use tunnel ticket and
validated gateway URL for every initial connection or reconnect. The CLI sends
that ticket only as one `Authorization: Bearer <ticket>` header on the
WebSocket upgrade to `/v1/connect`.

Tickets are never written to configuration, credential storage, logs, error
messages, or fixtures. The in-memory ticket value is cleared immediately after
the dial attempt. Static gateway tokens, query-parameter tickets, and direct
gateway registration are not supported.

RS256/JWKS validation, replay protection, lifecycle callbacks, and gateway
service credentials belong to the separate `mockingo-gateway` repository.
