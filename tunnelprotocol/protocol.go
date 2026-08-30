// Package tunnelprotocol defines the versioned JSON messages exchanged by the
// Mockingo gateway and local agent. It contains no network, authentication, or
// service implementation.
package tunnelprotocol

const (
	// Version is the on-wire tunnel protocol version. It is independent of the
	// agent module's semantic version.
	Version = 1

	// MaxBodySize is the maximum decoded HTTP body carried by protocol v1.
	MaxBodySize = 10 << 20 // 10 MiB
	// MaxMessageSize accounts for base64 and JSON envelope overhead.
	MaxMessageSize = MaxBodySize*4/3 + 1<<20
	// MaxDependencyMessageSize is enforced independently by the Gateway capture
	// socket so an authenticated Agent cannot allocate arbitrary event payloads.
	MaxDependencyMessageSize       = 2 << 20
	MaxDependencyRequestPreview    = 256 << 10
	MaxDependencyResponsePreview   = 512 << 10
	MaxDependencyHeaderBytes       = 256 << 10
	MaxDependencyConfigMessageSize = 16 << 20
	MaxDependencyBehaviors         = 1000
	MaxDependencyReplayBody        = 1 << 20
)

const MaxTCPFramePayload = 32 * 1024
