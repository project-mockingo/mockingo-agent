package endpoint

import (
	"context"
)

// Catalog is the gateway's transitional, read-only endpoint dependency. It is
// used only to preserve public 404 endpoint_not_found versus 502 tunnel_offline
// semantics. The gateway does not own endpoint writes, schema migrations, or
// ownership decisions.
type Catalog interface {
	ExistsByHostname(context.Context, string) (bool, error)
	Ping(context.Context) error
	Close()
}
