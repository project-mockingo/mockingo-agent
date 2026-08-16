# Local mock engine

`mockingo mock` publishes static local mocks through the normal Mockingo HTTP
tunnel:

```text
WireMock mappings ─┐
                   ├─> MockDefinition[] -> MockEngine
OpenAPI document ──┘                         |
                                              v
                              embedded 127.0.0.1 HTTP server
                                              |
                                              v
                          existing local forwarder and protocol v1 tunnel
```

The gateway sees an ordinary local HTTP target. There are no mock-specific
frames, protocol versions, control-plane fields, or remote mock definitions.
The backend-returned `https://<name>.mockingo.click` URL remains authoritative.

## Commands

```bash
mockingo mock --name weather --wiremock ./wiremock
mockingo mock --name weather --openapi ./openapi.yaml
```

`--name` and exactly one source option are required. `--http` and `--port` are
not used: the engine binds an OS-assigned port on `127.0.0.1` only. Source
loading and validation finish before the local server and tunnel session are
created.

The command uses the same Clerk OAuth credentials, refresh path, control-plane
tunnel-session API, one-use gateway ticket, reconnect loop, header filtering,
body limits, and shutdown context as `mockingo expose`. On reconnect, the mock
engine and its loopback listener stay running; only the tunnel session changes.

Unmatched requests return:

```http
HTTP/1.1 404 Not Found
Content-Type: application/json

{"code":"mock_not_found","message":"No mock matched this request."}
```

Standalone mock mode never forwards unmatched requests. Hybrid mock-first,
localhost-fallback behavior is deferred to M2.

## Runtime and limits

- Definitions are cloned, sorted once, and read without locks at request time.
- Lower numeric priority wins; equal priority preserves deterministic load order.
- Supported methods are `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD`, and
  `OPTIONS`; WireMock also supports `ANY`.
- Static response bodies are capped at the protocol v1 10 MiB body limit.
- Mapping count and generated OpenAPI route count are capped at 10,000.
- Mapping files are capped at 5 MiB; each OpenAPI file is capped at 10 MiB.
- Fixed delay is capped at 30 seconds and is cancelled by client disconnect or
  application shutdown.
- OpenAPI example generation stops at depth 12 and safely truncates cycles.

Mappings and specifications are loaded once. M1 has no hot reload, recording,
request journal, verification API, custom Mockingo DSL, or admin endpoint.
