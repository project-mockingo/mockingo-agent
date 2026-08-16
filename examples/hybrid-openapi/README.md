# Hybrid expose with a partial OpenAPI document

The real application implements `/users` and `/orders`. The partial OpenAPI
document describes only `/weather` and `/weather/{city}`, so those operations
are mocked while all other requests fall through to the application.

```bash
go run ./examples/hybrid-openapi/app
```

```bash
mockingo expose --name hybrid-openapi --http 8080 \
  --openapi ./examples/hybrid-openapi/partial-api.yaml --verbose
```

```bash
curl https://hybrid-openapi.mockingo.click/users
curl -X POST https://hybrid-openapi.mockingo.click/orders
curl https://hybrid-openapi.mockingo.click/weather
curl https://hybrid-openapi.mockingo.click/weather/Prague
```

The first two requests are `FORWARD`; the OpenAPI operations are `MOCK`.
