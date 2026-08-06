# Route 53 DNS and wildcard TLS

Create only these records in the `mockingo.click` hosted zone:

```text
A api.mockingo.click   <SERVER_PUBLIC_IP>
A *.mockingo.click     <SERVER_PUBLIC_IP>
```

Use an AWS Elastic IP so server replacement or reboot does not change the address. If IPv6 is configured, add:

```text
AAAA api.mockingo.click   <SERVER_IPV6>
AAAA *.mockingo.click     <SERVER_IPV6>
```

Do not create a record per endpoint. Wildcard DNS sends all one-label endpoint hosts to Caddy; the gateway resolves the persistent endpoint from the original HTTP `Host`. Endpoint creation never calls AWS APIs.

The default Caddy image does not include Route 53 support. `deploy/Dockerfile.caddy` pins Caddy and builds it with the pinned `github.com/caddy-dns/route53` module. `deploy/Caddyfile` obtains the apex-host and wildcard certificate with Route 53 DNS-01 and redirects normal HTTP traffic to HTTPS automatically.

## IAM permissions

Prefer an EC2 IAM role. Static `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` values are supported by the AWS credential chain but should be a last resort. Configure `AWS_REGION` (for example `eu-central-1`). Never place credentials in the image, Caddyfile, Compose file, or repository.

A minimum practical policy needs hosted-zone discovery/read access and permission to change records in the one hosted zone. Replace the account-independent hosted-zone ID:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "route53:GetChange",
        "route53:ListHostedZonesByName",
        "route53:ListResourceRecordSets"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": "route53:ChangeResourceRecordSets",
      "Resource": "arn:aws:route53:::hostedzone/REPLACE_HOSTED_ZONE_ID"
    }
  ]
}
```

AWS requires `GetChange` against change resources and hosted-zone listing actions do not support useful resource scoping, hence the first statement uses `*`. Scope the mutation permission to the Mockingo hosted zone. Restrict the role further with Route 53 record-name/type condition keys if compatible with the selected DNS provider release.
