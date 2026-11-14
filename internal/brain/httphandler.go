// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxRequestBodyBytes is the largest MCP request body the brain's HTTP
// handler reads. go-sdk v1.8.0 started refusing any body over 4 MiB by
// default, which is below the brain's own caps (maxPushBytes, 64 MiB, and
// maxArtifactBytes). Those caps belong to the tools, which refuse with an
// error that names the limit. The transport limit only has to keep a request
// that no tool would accept from being read into memory. It is twice the
// push cap plus 1 MiB for the JSON envelope, because JSON escaping can make
// a file's encoded form larger than its bytes (a quote or newline costs
// two). A push whose encoded form exceeds this is refused by the transport
// with a 413 even if its decoded bytes are under maxPushBytes.
const MaxRequestBodyBytes = 2*maxPushBytes + 1<<20

// NewHTTPHandler is the one way the brain is served over HTTP, so the daemon
// and the tests share the same transport limits.
//
// The brain listens on 127.0.0.1 behind a reverse proxy or tunnel: requests
// arrive via localhost but carry the public Host header (your
// CORRALAI_ALLOWED_HOSTS), which the go-sdk's localhost DNS-rebinding guard
// rejects with 403. That guard defends a *local* MCP server against malicious
// web pages; here the brain is proxy-fronted AND OIDC-authenticated (a
// browser can't forge the Bearer token), so the guard only false-positives
// real client traffic. It is disabled.
func NewHTTPHandler(srv *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			DisableLocalhostProtection: true,
			MaxRequestBodyBytes:        MaxRequestBodyBytes,
		},
	)
}
