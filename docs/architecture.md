# Architecture

```text
Public HTTP client
        |
        v
Mockingo Gateway (hostname routing)
        |
        | outbound WebSocket tunnel
        v
Mockingo CLI / Agent
        |
        v
127.0.0.1:8080
        |
        v
User application
```

The CLI always initiates the network connection. The user application remains on the local machine, and the gateway never connects directly to the user's private network.

## Control plane

The default path has the CLI authenticate to the Spring Boot backend at `api.mockingo.com`, obtain a short-lived RS256 tunnel ticket from `POST /api/v1/tunnel-sessions`, and open `gateway.mockingo.com/v1/connect`. The Go gateway validates locally against cached backend JWKS and reports lifecycle callbacks. Endpoint ownership comes only from the Clerk OAuth subject; the CLI sends no owner identity.

OAuth access tokens go only to Spring Boot. Each returned tunnel ticket goes only to the backend-selected, locally validated gateway URL and is consumed by one WebSocket dial attempt. After connection loss, the CLI never reuses the ticket: it waits with bounded jittered backoff, requests a new backend session, and dials with the new ticket. Temporary `tunnel_session_pending` and `endpoint_already_connected` races are retried while the previous gateway callback completes.

The old `POST /api/v1/tunnels` static-token path remains only behind explicit `mockingo expose --legacy` compatibility mode and never receives OAuth credentials.

Endpoint reservations live in the Spring Boot control plane and remain until explicitly deleted. A connected endpoint is exclusive. Stopping the CLI takes the endpoint offline without deleting it. A disconnected ticket session never reconnects with its old credential; the CLI creates a fresh authenticated backend session for the same persistent endpoint and hostname.

The gateway's ticket registry is indexed atomically by backend endpoint ID, session ID, endpoint name, and hostname. Internal management never uses endpoint name as identity. See [tunnel ticket authentication](tunnel-ticket-auth.md), [gateway internal API](gateway-internal-api.md), and [backend callbacks](backend-callbacks.md).

## Data plane

Protocol version 1 uses JSON-over-WebSocket. A gateway request message carries a unique request ID, HTTP method, path and query, filtered headers, and a base64 body. The agent calls only `http://127.0.0.1:<user-selected-port>` and returns a response message with the same ID, status, filtered headers, and body.

The gateway maintains a synchronized map from request IDs to waiting public requests. Both gateway and agent use one socket reader and serialize socket writes. Local HTTP calls run concurrently, so one tunnel can serve multiple simultaneous requests.

Hop-by-hop headers are removed in both directions. The gateway adds `X-Forwarded-Host`, `X-Forwarded-Proto`, and `X-Forwarded-For`. Redirect following is disabled in the agent, preserving the local application's original redirect response.

## Limits and failure mapping

Request and response bodies are buffered in memory and capped at 10 MiB. Oversized public requests receive `413 Payload Too Large`. A missing or failed local service receives `502 Bad Gateway`; request deadlines receive `504 Gateway Timeout`. A disconnected tunnel returns `502` until it reconnects.

The protocol does not support streaming, SSE, or application WebSocket forwarding. Caddy terminates production TLS/WSS with a wildcard certificate obtained through Route 53 DNS-01.

Domain roles are strict: `api.mockingo.com` is Spring Boot, `gateway.mockingo.com` is the Go gateway control/WebSocket host, and `<name>.mockingo.click` is the stable public tunnel host. The bare `mockingo.click` domain is not a backend, frontend, or gateway service URL.

## Process lifecycle

With a command, the CLI starts it without shell interpolation, forwards stdout and stderr immediately, and waits for readiness. Unix children start in a process group; Windows children start in a new process group and batch files use `cmd.exe /C`. On cancellation, the CLI attempts graceful process-tree termination, waits briefly, and then forces remaining descendants to exit.
