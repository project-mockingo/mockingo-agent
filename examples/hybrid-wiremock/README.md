# Hybrid expose with WireMock mappings

The example application implements the real `/users` and `/orders` routes.
WireMock mappings replace `/weather` and `/payment` through the same public
endpoint.

From the repository root, start the real application:

```bash
go run ./examples/hybrid-wiremock/app
```

In another terminal, expose it with the mappings:

```bash
mockingo expose --name hybrid-wiremock --http 8080 \
  --wiremock ./examples/hybrid-wiremock/wiremock --verbose
```

Verify both paths (replace the hostname if the endpoint name differs):

```bash
curl https://hybrid-wiremock.mockingo.click/users
curl -X POST https://hybrid-wiremock.mockingo.click/orders -d '{"sku":"book"}'
curl https://hybrid-wiremock.mockingo.click/weather
curl -i -X POST https://hybrid-wiremock.mockingo.click/payment
```

The first two requests are logged as `FORWARD`; the last two are logged as
`MOCK`. The application prints each real request, so it also demonstrates that
the mocked routes never reach localhost.
