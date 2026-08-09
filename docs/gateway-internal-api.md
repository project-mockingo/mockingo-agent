# Gateway internal API

The Spring backend calls these endpoints on `https://gateway.mockingo.com`. Every request requires exactly one `Authorization: Bearer <MOCKINGO_GATEWAY_INTERNAL_TOKEN>` header. This token is distinct from Clerk tokens, tunnel tickets, the callback token, and the legacy management token. Comparison is constant-time and failures are generic `401` responses.

## Batch status

`POST /internal/v1/tunnels/status`

```json
{"endpointIds":["e9949642-8b35-4247-ac5d-c076a463058d"]}
```

```json
{
  "statuses": {
    "e9949642-8b35-4247-ac5d-c076a463058d": {
      "status": "connected",
      "sessionId": "8db7aeef-a927-48d1-9190-99076fbe3c71",
      "protocol": "http",
      "localPort": 8080,
      "connectedAt": "2026-08-09T12:00:00Z"
    }
  }
}
```

The body is capped at 1 MiB. IDs must be UUIDs, are deduplicated, and are limited by `MOCKINGO_INTERNAL_STATUS_MAX_BATCH` (default 500). One registry read returns every requested ID as `connected` or `offline`; no PostgreSQL or backend calls occur. Responses use `Cache-Control: no-store` and never expose users, names, addresses, or credentials.

Status describes only this gateway process. A multi-replica deployment needs external aggregation or sticky endpoint ownership.

## Disconnect

`POST /internal/v1/tunnels/{endpointId}/disconnect`

The endpoint ID must be a UUID. An active socket receives a graceful close, normal atomic cleanup runs, and ticket tunnels send one disconnected callback with reason `internal_disconnect`. Active and already-offline endpoints both return `204 No Content`; repeated calls are idempotent. Endpoint names are not accepted.

## Route isolation

Caddy routes `gateway.mockingo.com/internal/*` to the gateway, where bearer authentication is always enforced. Network/firewall restriction is recommended as defense in depth. On a public host such as `spring-demo.mockingo.click`, `/internal/...` remains ordinary user application traffic and is sent through the tunnel; Host routing cannot bypass internal authentication.

Gateway JSON errors include `Content-Type: application/json`, `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, and `X-Request-ID`. They do not contain token or backend details.
