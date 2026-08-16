# WireMock compatibility in M1

WireMock JSON is an interoperability input format. Mockingo implements the
subset below natively in Go; it does not run Java, a WireMock process, Docker,
or the WireMock Admin API. Unsupported or unknown mapping fields fail startup
instead of being ignored.

| Feature | M1 |
|---|:---:|
| `request.method` | ✓ |
| `request.url` (path and exact query string) | ✓ |
| `request.urlPath` (query ignored) | ✓ |
| `request.urlPathTemplate` | ✓ |
| `ANY` method | ✓ |
| `priority` | ✓ |
| `response.status` | ✓ |
| `response.headers` string and string-array values | ✓ |
| `response.body` | ✓ |
| `response.jsonBody` | ✓ |
| `response.bodyFileName` | ✓ |
| `response.fixedDelayMilliseconds` | ✓ |
| header matching | ✗ |
| structured query matching | ✗ |
| cookies and basic auth matching | ✗ |
| `bodyPatterns`, JSONPath, XPath, JSON/XML equality | ✗ |
| multipart and logical/custom matchers | ✗ |
| scenarios and state | ✗ |
| templating and transformers | ✗ |
| faults, random delay, dribble delay | ✗ |
| callbacks, webhooks, and proxying | ✗ |
| recording, request journal, and verification | ✗ |
| WireMock Admin API | ✗ |

## Layout and loading

Directory input uses the standard layout:

```text
wiremock/
  mappings/*.json
  __files/**
```

Mapping JSON files are sorted by path and all are parsed and validated before
the server starts. A direct mapping JSON file is also accepted. Its associated
body root is the sibling `__files` directory, or the project `__files` directory
when the file is directly inside `mappings`.

Only one of `body`, `jsonBody`, and `bodyFileName` may be present. `jsonBody`
preserves JSON and defaults `Content-Type` to `application/json`. Body files are
read as bytes, so binary responses are supported.

`bodyFileName` must resolve beneath `__files`. Absolute paths, drive/device
paths, `..` traversal, missing files, and symlinks escaping that root fail
startup. Absolute source paths are never returned to remote callers.

Status must be from 100 through 599. Priority defaults to WireMock's normal
value of 5; smaller values match first. Fixed delay must be non-negative and no
more than 30 seconds.
