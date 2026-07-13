// SPDX-License-Identifier: Elastic-2.0

package gateway

import (
	"github.com/pdbethke/corralai/internal/netguard"
)

// Guard defends the brain against SSRF when it dials user-registered upstreams.
// It is a type alias for netguard.Guard so existing gateway call sites don't
// change; the resolve-and-pin logic itself now lives in internal/netguard so
// browser/forge can reuse it without depending on gateway.
type Guard = netguard.Guard

// NewGuard builds a guard; allowHosts (hostnames or IP literals) bypass the block.
var NewGuard = netguard.NewGuard
