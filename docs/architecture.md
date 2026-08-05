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

`mockingo login` stores the gateway URL and API token in the operating system's user configuration directory. `mockingo expose` waits for the selected local port and calls `POST /api/v1/tunnels`. The gateway validates the name and returns a public URL, WebSocket connection URL, and a cryptographically random session token. The API token and tunnel session token are separate credentials.

Registrations live in gateway memory. A connected name is exclusive. On normal shutdown, the CLI deletes its registration. After an unexpected socket loss, that registration accepts the same session token for five minutes so the agent can reconnect without changing its public hostname.

## Data plane

Protocol version 1 uses JSON-over-WebSocket. A gateway request message carries a unique request ID, HTTP method, path and query, filtered headers, and a base64 body. The agent calls only `http://127.0.0.1:<user-selected-port>` and returns a response message with the same ID, status, filtered headers, and body.

The gateway maintains a synchronized map from request IDs to waiting public requests. Both gateway and agent use one socket reader and serialize socket writes. Local HTTP calls run concurrently, so one tunnel can serve multiple simultaneous requests.

Hop-by-hop headers are removed in both directions. The gateway adds `X-Forwarded-Host`, `X-Forwarded-Proto`, and `X-Forwarded-For`. Redirect following is disabled in the agent, preserving the local application's original redirect response.

## Limits and failure mapping

Request and response bodies are buffered in memory and capped at 10 MiB. Oversized public requests receive `413 Payload Too Large`. A missing or failed local service receives `502 Bad Gateway`; request deadlines receive `504 Gateway Timeout`. A disconnected tunnel returns `502` until it reconnects.

The protocol does not support streaming, SSE, or application WebSocket forwarding. TLS termination and certificate management belong to an external reverse proxy in front of the reference gateway.

## Process lifecycle

With a command, the CLI starts it without shell interpolation, forwards stdout and stderr immediately, and waits for readiness. Unix children start in a process group; Windows children start in a new process group and batch files use `cmd.exe /C`. On cancellation, the CLI attempts graceful process-tree termination, waits briefly, and then forces remaining descendants to exit.
