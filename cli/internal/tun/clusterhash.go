package tun

import (
	"crypto/sha1"
	"encoding/hex"
)

// ClusterHash is the short, filesystem-safe id for a cluster key (host:port).
//
// It lives here rather than beside the registry that made it, because the
// registry is a macOS and Windows file and this id is not. `plug --dockerrun`
// names its sidecar container with it on every platform, and one cluster must
// answer to one id whoever asks: a second spelling of the same hash is how two
// pieces of code end up disagreeing about which cluster they are talking about.
func ClusterHash(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:8])
}
