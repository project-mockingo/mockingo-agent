# OpenAPI mocking in M1

OpenAPI is an importer, not part of the request hot path:

```text
OpenAPI YAML/JSON -> parse and resolve -> validate -> MockDefinition[]
```

Mockingo uses `github.com/getkin/kin-openapi` for OpenAPI 3.x parsing,
resolution, and validation. The `servers` field is contract metadata only and
is never contacted or used as an upstream.

## Routes and responses

`GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD`, and `OPTIONS` operations become
static routes. A path such as `/users/{id}` matches exactly one non-empty path
segment for `{id}`; parameter schema types are not enforced in M1.

One response is chosen at startup:

1. exact `200`;
2. the lowest explicit 2xx status;
3. `default` (served as status 200);
4. the lowest explicit numeric status.

Response headers with an explicit example, named example, or schema-derived
static value are compiled into the mock response using the same deterministic
example rules.

One response media type is chosen without runtime `Accept` negotiation:

1. `application/json`;
2. the lexically first `application/*+json` media type;
3. the lexically first remaining media type.

Lexical ordering is used for the final two rules because the parser represents
content as a Go map and does not retain declaration order.

The selected media type's body uses this precedence:

1. explicit media-type `example`;
2. lexically first named example;
3. schema `example`;
4. schema `default`;
5. first enum value;
6. a deterministic generated schema value;
7. empty body.

Generated primitives are `"string"`, `0`, `0`, and `false`. Arrays contain one
generated item. Objects contain their properties in deterministic key order in
the encoded JSON. Recursion and deep graphs are truncated after depth 12 with a
local warning; generation never follows them indefinitely.

## References and non-runtime concepts

Fragment references and relative local-file references beneath the top-level
document directory are supported. Each referenced file is size-limited and
symlink-resolved. References outside that directory, absolute remote hosts, and
HTTP(S) references fail startup. No network fetch is attempted.

M1 does not enforce OpenAPI security, request schemas, parameters, headers, or
authentication. It does not execute callbacks, webhooks, links, external
examples, or server URLs.
