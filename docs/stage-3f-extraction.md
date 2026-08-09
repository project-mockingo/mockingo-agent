# Stage 3F CLI extraction record

Gateway and protocol sources were removed after the extracted protocol and
gateway suites passed. The last monorepo source commit is
`3fe69f8ad4ccdf41bfa5e90fa272a7d45cfbe5af`, preserved locally as tag
`stage-3f-monorepo-backup-20260809`.

Rollback uses the previous CLI release and gateway image. Restore the tagged
monorepo source only when a source rollback is necessary. The new gateway must
not be run concurrently with the old gateway against the same endpoint
registry unless session coordination is explicitly supported.
