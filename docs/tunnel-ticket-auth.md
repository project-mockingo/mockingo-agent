# Tunnel ticket authentication

`GET /v1/connect` accepts exactly one `Authorization: Bearer <ticket>` header on a WebSocket upgrade. The existing backend-issued JWT contract and protocol v1 wire format are unchanged.

The gateway validates RS256, `kid`, cached JWKS, issuer, audience, temporal claims, `jti`/session ID, endpoint ID/name, owner subject, protocol, local port, and protocol version. A bounded replay cache consumes the ticket before the WebSocket becomes active. Endpoint/session/name collisions are rejected. Successful connections and terminal disconnects produce backend callbacks; trusted rejected sessions produce rejected callbacks.

Malformed values, static tokens, query-parameter values, duplicate authorization headers, expired tickets, and tickets with invalid claims receive a generic authentication error. Credentials are never logged.

Required settings:

- `MOCKINGO_BACKEND_URL`
- `MOCKINGO_BACKEND_JWKS_URL`
- `MOCKINGO_TUNNEL_TICKET_ISSUER`
- `MOCKINGO_TUNNEL_TICKET_AUDIENCE`
- `MOCKINGO_BACKEND_CALLBACK_TOKEN`
- `MOCKINGO_GATEWAY_INTERNAL_TOKEN`
- `MOCKINGO_GATEWAY_INSTANCE_ID`
