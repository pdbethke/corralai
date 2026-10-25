// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// mcpProtocolVersion is what CheckMCP offers; corral's server (the Go MCP SDK)
// negotiates down from it if it must.
const mcpProtocolVersion = "2025-06-18"

// wantedTools are the tools `corral mcp` serves; a server answering without
// them is some other program.
var wantedTools = []string{"list_audit_findings", "get_audit_finding"}

type rpcMsg struct {
	ID     *int            `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// stderrTail bounds how much of the server's stderr a CheckMCP failure
// carries: the end, where a startup failure says why.
const stderrTail = 2 << 10

// tailBuffer keeps the last max bytes written to it. exec copies the child's
// stderr into it from its own goroutine, so it is locked.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
	cut bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b, t.cut = t.b[over:], true
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.TrimSpace(string(t.b))
	if t.cut && s != "" {
		s = "..." + s
	}
	return s
}

// CheckMCP proves `<bin> mcp` answers: it starts the server, speaks JSON-RPC
// over its stdio (initialize, notifications/initialized, tools/list) and
// requires the findings tools. This is what an agent will do on its first
// launch, so a pass here means the registration points at something that works.
// The child is killed afterwards and the whole exchange is bounded by Timeout.
//
// A failure carries the last stderrTail bytes of the server's stderr: a server
// that will not start says why there, and "closed its output" alone would drop
// that reason on the floor.
func CheckMCP(ctx context.Context, bin string) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "mcp") // #nosec G204 -- the binary setup just registered, fixed argv, no shell
	stderr := &tailBuffer{max: stderrTail}
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second // a grandchild holding stderr must not hold the check open
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s mcp: %w", bin, err)
	}
	err = handshake(ctx, bin, in, out)
	// Stop the child and wait for it, so stderr has been copied in full
	// before it is read.
	cancel()
	_ = in.Close()
	_ = cmd.Wait()
	if err != nil {
		if tail := stderr.String(); tail != "" {
			err = fmt.Errorf("%w; its stderr: %s", err, tail)
		}
	}
	return err
}

// handshake is CheckMCP's JSON-RPC exchange over the child's stdio.
func handshake(ctx context.Context, bin string, in io.Writer, out io.Reader) error {

	msgs := make(chan rpcMsg)
	go func() {
		defer close(msgs)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			var m rpcMsg
			if json.Unmarshal(sc.Bytes(), &m) == nil && m.ID != nil {
				select {
				case msgs <- m:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	send := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = io.WriteString(in, string(b)+"\n")
		return err
	}
	call := func(id int, method string, params any) (json.RawMessage, error) {
		if err := send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		for {
			select {
			case m, ok := <-msgs:
				if !ok {
					return nil, fmt.Errorf("%s mcp closed its output before answering %s", bin, method)
				}
				if *m.ID != id {
					continue
				}
				if m.Error != nil {
					return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
				}
				return m.Result, nil
			case <-ctx.Done():
				return nil, fmt.Errorf("%s mcp did not answer %s within %s", bin, method, Timeout)
			}
		}
	}

	if _, err := call(1, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "corral-setup", "version": "1"},
	}); err != nil {
		return err
	}
	if err := send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return err
	}
	raw, err := call(2, "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	var tl struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &tl); err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	have := map[string]bool{}
	for _, t := range tl.Tools {
		have[t.Name] = true
	}
	var missing []string
	for _, w := range wantedTools {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		return errors.New("corral mcp answered but does not serve " + strings.Join(missing, ", "))
	}
	return nil
}
