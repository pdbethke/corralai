package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/reposcan"
)

type fakeBackend struct {
	reply string
	err   error
	sent  []agentbackend.Message
}

func (f *fakeBackend) Chat(msgs []agentbackend.Message, tools []any) (agentbackend.Message, error) {
	f.sent = msgs
	if f.err != nil {
		return agentbackend.Message{}, f.err
	}
	return agentbackend.Message{Role: "assistant", Content: f.reply}, nil
}

func TestLLMDeriverReturnsTheGoalText(t *testing.T) {
	fb := &fakeBackend{reply: "  must reject negative balances  "}
	d := llmDeriver{b: fb}

	text, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "pkg/a.go", Lang: "go"}, "package pkg\n")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if strings.TrimSpace(text) != "must reject negative balances" {
		t.Errorf("text = %q", text)
	}
	// The source must actually be in the request, and nothing else should be.
	var joined string
	for _, m := range fb.sent {
		joined += m.Content
	}
	if !strings.Contains(joined, "package pkg") {
		t.Error("the source was not sent to the model")
	}
}

// An empty or refusing reply is the file's property, not an outage.
func TestLLMDeriverEmptyReplyIsNotAnError(t *testing.T) {
	d := llmDeriver{b: &fakeBackend{reply: "   "}}
	_, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "a.go"}, "x")
	if err != nil {
		t.Fatalf("empty reply must not be an error: %v", err)
	}
	if ok {
		t.Fatal("empty reply must be ungoaled")
	}
}

// A transport failure must surface as an error so it becomes derive-failed,
// never ungoaled.
func TestLLMDeriverTransportFailureIsAnError(t *testing.T) {
	d := llmDeriver{b: &fakeBackend{err: errors.New("connection refused")}}
	if _, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "a.go"}, "x"); err == nil || ok {
		t.Fatalf("want an error and ok=false, got ok=%v err=%v", ok, err)
	}
}
