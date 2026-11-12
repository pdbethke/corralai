// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/models"
)

// seenReq records the last request a fake endpoint saw.
type seenReq struct {
	path, auth, body string
}

func fakeEndpoint(t *testing.T, reply string) (*httptest.Server, *seenReq) {
	t.Helper()
	c := &seenReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		raw, _ := json.Marshal(b)
		c.path, c.auth, c.body = r.URL.Path, r.Header.Get("Authorization"), string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

var userMsg = []Message{{Role: "user", Content: "hi"}}

func TestBuildOllamaWireHitsApiChat(t *testing.T) {
	srv, c := fakeEndpoint(t, `{"message":{"role":"assistant","content":"ok"}}`)
	b, err := Build(ResolvedSeat{Flag: "writer-model", Spec: "m", Kind: "api", Wire: models.WireOllama, Endpoint: srv.URL}, SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Chat(userMsg, nil); err != nil {
		t.Fatal(err)
	}
	if c.path != "/api/chat" || !strings.Contains(c.body, `"model":"m"`) {
		t.Fatalf("path=%q body=%s", c.path, c.body)
	}
}

func TestBuildOpenAIChatWireHitsChatCompletions(t *testing.T) {
	srv, c := fakeEndpoint(t, `{"choices":[{"message":{"content":"ok"}}]}`)
	b, err := Build(ResolvedSeat{Flag: "writer-model", Spec: "m", Kind: "api", Provider: "vllm", Wire: models.WireOpenAIChat, Endpoint: srv.URL + "/v1"}, SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Chat(userMsg, nil); err != nil {
		t.Fatal(err)
	}
	if c.path != "/v1/chat/completions" || c.auth != "" {
		t.Fatalf("path=%q auth=%q", c.path, c.auth)
	}
}

func TestBuildHostedGatewayUsesKeyEnv(t *testing.T) {
	srv, c := fakeEndpoint(t, `{"choices":[{"message":{"content":"ok"}}]}`)
	rs := ResolvedSeat{Flag: "writer-model", Spec: "m", Kind: "api", Provider: "openai", Wire: models.WireHosted, Endpoint: srv.URL + "/v1", KeyEnv: "PROXY_KEY"}

	t.Setenv("PROXY_KEY", "")
	if _, err := Build(rs, SeatOptions{}); err == nil || !strings.Contains(err.Error(), "PROXY_KEY") {
		t.Fatalf("unset key: want error naming PROXY_KEY, got %v", err)
	}

	t.Setenv("PROXY_KEY", "k")
	b, err := Build(rs, SeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Chat(userMsg, nil); err != nil {
		t.Fatal(err)
	}
	if c.auth != "Bearer k" || c.path != "/v1/chat/completions" {
		t.Fatalf("path=%q auth=%q", c.path, c.auth)
	}
}

// A hosted seat with no endpoint is built for its RESOLVED provider, never for
// what its model name looks like.
func TestBuildHostedVendorUsesTheResolvedProvider(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	_, want := ForVendor("anthropic", "claude-x")
	if want == nil {
		t.Skip("an Anthropic key is resolvable on this host")
	}
	_, got := Build(ResolvedSeat{Flag: "writer-model", Spec: "claude-x", Kind: "api", Provider: "anthropic", Wire: models.WireHosted}, SeatOptions{})
	if got == nil || got.Error() != want.Error() || !strings.Contains(got.Error(), "needs an Anthropic key") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildAgentSeatDefersToForSeat(t *testing.T) {
	wantB, wantErr := ForSeat("codex", SeatOptions{})
	gotB, gotErr := Build(ResolvedSeat{Flag: "writer-model", Spec: "codex", Kind: "agent"}, SeatOptions{})
	if (wantErr == nil) != (gotErr == nil) || (wantErr != nil && wantErr.Error() != gotErr.Error()) {
		t.Fatalf("err: got %v want %v", gotErr, wantErr)
	}
	if reflect.TypeOf(gotB) != reflect.TypeOf(wantB) {
		t.Fatalf("type: got %T want %T", gotB, wantB)
	}
}

func TestBuildRefusesLocalWithoutEndpoint(t *testing.T) {
	for _, w := range []models.Wire{models.WireOllama, models.WireOpenAIChat} {
		_, err := Build(ResolvedSeat{Flag: "critic-model", Spec: "m", Kind: "api", Wire: w}, SeatOptions{})
		if err == nil || !strings.Contains(err.Error(), "critic-model") {
			t.Fatalf("wire %s: want error naming the flag, got %v", w, err)
		}
	}
}

func TestDescribe(t *testing.T) {
	got := ResolvedSeat{Spec: "gpt-oss:120b", Provider: "ollama", Endpoint: "http://127.0.0.1:11434", DeclaredBy: DeclaredDiscovered}.Describe()
	if got != "ollama/gpt-oss:120b @ http://127.0.0.1:11434 (discovered)" {
		t.Fatal(got)
	}
}
