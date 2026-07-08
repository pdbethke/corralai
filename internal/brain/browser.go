// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/taskartifacts"
)

type BrowserManager struct {
	mu       sync.Mutex
	browser  *rod.Browser
	pages    map[string]*rod.Page // agentName -> page
	selfAddr string               // the brain's own listen host:port (blocked as an SSRF target)
}

// NewBrowserManager builds the agent browser. selfAddr is the brain's own
// listen address (CORRALAI_ADDR) — the browser refuses to navigate there so a
// hijacked agent can't loop back into the brain's admin/MCP surface.
func NewBrowserManager(selfAddr string) *BrowserManager {
	return &BrowserManager{
		pages:    make(map[string]*rod.Page),
		selfAddr: selfAddr,
	}
}

// metadataHosts are cloud instance-metadata (IMDS) hostnames — never a
// legitimate target for the agent browser.
var metadataHosts = map[string]bool{
	"metadata.google.internal": true,
	"metadata":                 true,
}

// blockedHost reports whether host (a hostname or IP literal, no port) is an
// SSRF-sensitive target the agent browser must never reach: cloud-metadata
// endpoints (IMDS) and link-local addresses (169.254.0.0/16, fe80::/10).
// Loopback and private ranges are deliberately ALLOWED so a tester can drive
// the app the herd just built on localhost — the brain's own address is blocked
// separately, by host:port (see isBrainAddr).
func blockedHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if metadataHosts[h] || h == "100.100.100.100" /* Alibaba IMDS */ {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return true
		}
	}
	return false
}

// isBrainAddr reports whether u targets the brain's own listen address — a
// loopback host on the brain's port. The app under test runs on a DIFFERENT
// port, so this blocks the brain without impeding legitimate localhost testing.
func isBrainAddr(u *url.URL, selfAddr string) bool {
	if selfAddr == "" {
		return false
	}
	_, selfPort, err := net.SplitHostPort(selfAddr)
	if err != nil || selfPort == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	if port != selfPort {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// guardNavigateURL validates a navigation target for the agent browser. Only
// http/https is allowed (blocks file://, chrome://, data:, etc.); cloud-metadata
// and link-local hosts and the brain's own address are refused. Applied to the
// requested URL AND re-applied to the final URL after redirects.
func guardNavigateURL(raw, selfAddr string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("blocked url scheme %q — the agent browser only navigates http/https", u.Scheme)
	}
	if blockedHost(u.Hostname()) {
		return fmt.Errorf("blocked: %q is a cloud-metadata / link-local endpoint, off-limits to the agent browser", u.Hostname())
	}
	if isBrainAddr(u, selfAddr) {
		return fmt.Errorf("blocked: the brain's own address is off-limits to the agent browser")
	}
	return nil
}

func (bm *BrowserManager) getPage(agent string) (*rod.Page, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.browser == nil {
		l := launcher.New().
			Headless(true).
			NoSandbox(true)

		u, err := l.Launch()
		if err != nil {
			return nil, fmt.Errorf("failed to launch chromium: %w", err)
		}

		bm.browser = rod.New().ControlURL(u)
		if err := bm.browser.Connect(); err != nil {
			bm.browser = nil // don't leave a non-nil, unconnected browser — the next call must relaunch
			return nil, fmt.Errorf("failed to connect to chromium: %w", err)
		}
	}

	page, ok := bm.pages[agent]
	if !ok {
		var err error
		page, err = bm.browser.Page(proto.TargetCreateTarget{URL: ""})
		if err != nil {
			return nil, fmt.Errorf("failed to create page: %w", err)
		}
		bm.pages[agent] = page
	}
	return page, nil
}

func (bm *BrowserManager) Close() {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if bm.browser != nil {
		_ = bm.browser.Close()
	}
}

func registerBrowser(s *mcp.Server, q *queue.Store, artStore *taskartifacts.Store, bm *BrowserManager) {
	if q == nil || artStore == nil || bm == nil {
		return
	}

	type browserNavigateIn struct {
		Name string `json:"name" jsonschema:"the agent's fallback name"`
		URL  string `json:"url" jsonschema:"the destination URL to navigate to"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_navigate",
		Description: "Navigate the headless browser to a specified URL.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in browserNavigateIn) (*mcp.CallToolResult, okOut, error) {
		agent := identity(req, in.Name)

		// SSRF guard: refuse metadata / link-local / file:// / the brain's own
		// address BEFORE opening a page. Loopback+private stay allowed so the
		// tester can drive the app the herd just built on localhost.
		if err := guardNavigateURL(in.URL, bm.selfAddr); err != nil {
			log.Printf("browser: agent %q navigation refused: %v", agent, err)
			return nil, okOut{OK: false}, err
		}

		page, err := bm.getPage(agent)
		if err != nil {
			return nil, okOut{OK: false}, err
		}

		if err := page.Navigate(in.URL); err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("navigate failed: %w", err)
		}
		if err := page.WaitLoad(); err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("wait load failed: %w", err)
		}

		// Re-check the FINAL url: a redirect could have landed on a blocked
		// host (metadata/brain). Leave no blocked content on the page.
		if info, ierr := page.Info(); ierr == nil {
			if err := guardNavigateURL(info.URL, bm.selfAddr); err != nil {
				_ = page.Navigate("about:blank")
				log.Printf("browser: agent %q navigation blocked after redirect to %s", agent, info.URL)
				return nil, okOut{OK: false}, fmt.Errorf("navigation blocked after redirect: %w", err)
			}
		}

		log.Printf("browser: agent %q navigated to %s", agent, in.URL)
		return nil, okOut{OK: true}, nil
	})

	type browserClickIn struct {
		Name     string `json:"name" jsonschema:"the agent's fallback name"`
		Selector string `json:"selector" jsonschema:"CSS selector of the element to click"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_click",
		Description: "Click an element matched by CSS selector on the current page.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in browserClickIn) (*mcp.CallToolResult, okOut, error) {
		agent := identity(req, in.Name)
		page, err := bm.getPage(agent)
		if err != nil {
			return nil, okOut{OK: false}, err
		}

		el, err := page.Element(in.Selector)
		if err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("find selector %q failed: %w", in.Selector, err)
		}
		if err := el.Click("left", 1); err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("click selector %q failed: %w", in.Selector, err)
		}

		log.Printf("browser: agent %q clicked %q", agent, in.Selector)
		return nil, okOut{OK: true}, nil
	})

	type browserInputIn struct {
		Name     string `json:"name" jsonschema:"the agent's fallback name"`
		Selector string `json:"selector" jsonschema:"CSS selector of the input field"`
		Text     string `json:"text" jsonschema:"text content to input"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_input",
		Description: "Enter text into an input element matched by CSS selector.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in browserInputIn) (*mcp.CallToolResult, okOut, error) {
		agent := identity(req, in.Name)
		page, err := bm.getPage(agent)
		if err != nil {
			return nil, okOut{OK: false}, err
		}

		el, err := page.Element(in.Selector)
		if err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("find selector %q failed: %w", in.Selector, err)
		}
		if err := el.Input(in.Text); err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("input to selector %q failed: %w", in.Selector, err)
		}

		log.Printf("browser: agent %q input text to %q", agent, in.Selector)
		return nil, okOut{OK: true}, nil
	})

	type browserScreenshotIn struct {
		Name     string `json:"name" jsonschema:"the agent's fallback name"`
		TaskID   int64  `json:"task_id" jsonschema:"the active task ID"`
		Filename string `json:"filename" jsonschema:"name of the screenshot file, e.g. step_1.png"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_screenshot",
		Description: "Capture a screenshot of the current page and save it directly as a task artifact in the database.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in browserScreenshotIn) (*mcp.CallToolResult, okOut, error) {
		agent := identity(req, in.Name)

		// 1. Claim Authorization Check
		t, err := q.TaskByID(in.TaskID)
		if err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("lookup task %d: %w", in.TaskID, err)
		}
		if t == nil {
			return nil, okOut{OK: false}, fmt.Errorf("task %d not found", in.TaskID)
		}
		if t.Status != "claimed" || t.ClaimedBy != agent {
			return nil, okOut{OK: false}, fmt.Errorf("forbidden: task %d is not claimed by agent %q", in.TaskID, agent)
		}

		// 2. Resolve page
		page, err := bm.getPage(agent)
		if err != nil {
			return nil, okOut{OK: false}, err
		}

		// 3. Take screenshot (PNG format, fullpage=true)
		imgBytes, err := page.Screenshot(true, nil)
		if err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("screenshot failed: %w", err)
		}

		// 4. Save in dedicated SQLite database
		safeFilename := filepath.Base(in.Filename)
		id, err := artStore.SaveArtifact(t.MissionID, in.TaskID, agent, safeFilename, "image/png", imgBytes)
		if err != nil {
			return nil, okOut{OK: false}, fmt.Errorf("failed to save screenshot as artifact: %w", err)
		}

		log.Printf("browser: agent %q saved screenshot %d (%s) for task %d", agent, id, safeFilename, in.TaskID)
		return nil, okOut{OK: true}, nil
	})

	type browserGetHTMLIn struct {
		Name string `json:"name" jsonschema:"the agent's fallback name"`
	}

	type browserHTMLOut struct {
		HTML string `json:"html"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "browser_get_html",
		Description: "Retrieve the full HTML source code of the current page.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in browserGetHTMLIn) (*mcp.CallToolResult, browserHTMLOut, error) {
		agent := identity(req, in.Name)
		page, err := bm.getPage(agent)
		if err != nil {
			return nil, browserHTMLOut{}, err
		}

		html, err := page.HTML()
		if err != nil {
			return nil, browserHTMLOut{}, fmt.Errorf("get html failed: %w", err)
		}

		return nil, browserHTMLOut{HTML: html}, nil
	})
}
