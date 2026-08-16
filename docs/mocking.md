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

Standalone mock mode never forwards unmatched requests.

## Hybrid Expose

Hybrid expose loads and compiles the same M1 `MockDefinition` values before it
starts an optional child application, validates the local port, or requests a
tunnel session:

```bash
mockingo expose --name shop --http 8080 --wiremock ./wiremock
mockingo expose --name shop --http 8080 --openapi ./partial-api.yaml
```

```text
Public Request
      |
Mockingo Tunnel
      |
Agent
      |
Mock matcher
   +-------+---------+
 MATCH           NO MATCH
   |                 |
Mock response    localhost:8080
```

The routing rule is fixed: a match is authoritative, including a declared 4xx
or 5xx response, and never reaches localhost; a miss uses the normal expose
forwarder exactly once. An internal failure while rendering a matched mock
returns a safe 500 and does not fall back to the real service. `--wiremock` and
`--openapi` are mutually exclusive and no `--hybrid` or `--unmatched` flag is
needed.

The dispatcher renders matched definitions directly. It does not route hybrid
mocks through the standalone loopback server. The compiled read-only engine,
local application, and dispatcher survive gateway reconnects; only the tunnel
session and one-use ticket are renewed. With `--verbose`, local traffic output
labels responses as `MOCK` or `FORWARD` without logging bodies or credentials.

| Command | Match | No match |
|---|---|---|
| `mockingo expose --http 8080` | forward | forward |
| `mockingo expose --http 8080 --wiremock/--openapi` | mock | forward |
| `mockingo mock --wiremock/--openapi` | mock | `404 mock_not_found` |

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

Mappings and specifications are loaded once. M1/M2 have no hot reload, recording,
request journal, verification API, custom Mockingo DSL, or admin endpoint.
