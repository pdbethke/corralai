// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/auth"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/recordings"
)

func TestRecordingsToolsListQueryReplay(t *testing.T) {
	dir := t.TempDir()
	c, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	rec, err := recordings.Open(filepath.Join(dir, "recordings.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	if err := rec.Upsert(recordings.MissionMeta{
		Slug: "golden-run", MissionID: 7, Directive: "test replay",
		TaskCount: 2, DoneTaskCount: 2, FindingCount: 1, DurationSeconds: 10.5,
	}, []recordings.Event{
		{TS: 1, Kind: "task_created", Subject: "build#1"},
		{TS: 2, Kind: "task_done", Actor: "builder", Subject: "build#1"},
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(c, nil, Options{Recordings: rec}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	listRes, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "list_recordings"})
	if err != nil {
		t.Fatalf("list_recordings: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list_recordings tool error: %+v", listRes.Content)
	}
	var listOut listRecordingsOut
	b, _ := json.Marshal(listRes.StructuredContent)
	if err := json.Unmarshal(b, &listOut); err != nil {
		t.Fatalf("decode list result: %v (%s)", err, b)
	}
	if len(listOut.Recordings) != 1 || listOut.Recordings[0].Slug != "golden-run" {
		t.Fatalf("unexpected list output: %+v", listOut)
	}

	queryRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "query_recordings",
		Arguments: map[string]any{"report": "event_kinds"},
	})
	if err != nil {
		t.Fatalf("query_recordings: %v", err)
	}
	if queryRes.IsError {
		t.Fatalf("query_recordings tool error: %+v", queryRes.Content)
	}
	var queryOut queryRecordingsOut
	qb, _ := json.Marshal(queryRes.StructuredContent)
	if err := json.Unmarshal(qb, &queryOut); err != nil {
		t.Fatalf("decode query result: %v (%s)", err, qb)
	}
	if len(queryOut.Rows) == 0 {
		t.Fatalf("expected query rows, got %+v", queryOut)
	}

	replayRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_recording_replay",
		Arguments: map[string]any{"slug": "golden-run"},
	})
	if err != nil {
		t.Fatalf("get_recording_replay: %v", err)
	}
	if replayRes.IsError {
		t.Fatalf("get_recording_replay tool error: %+v", replayRes.Content)
	}
	var replayOut recordingReplayOut
	rb, _ := json.Marshal(replayRes.StructuredContent)
	if err := json.Unmarshal(rb, &replayOut); err != nil {
		t.Fatalf("decode replay result: %v (%s)", err, rb)
	}
	if replayOut.Mission == nil || replayOut.Mission.MissionID != 7 || len(replayOut.Events) != 2 {
		t.Fatalf("unexpected replay output: %+v", replayOut)
	}
}

func TestShareRecordingRefusesDelegationToken(t *testing.T) {
	dir := t.TempDir()
	c, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	rec, err := recordings.Open(filepath.Join(dir, "recordings.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	if err := rec.Upsert(recordings.MissionMeta{
		Slug: "golden-run", MissionID: 7, SharedBy: "boss@x.com",
	}, []recordings.Event{{TS: 1, Kind: "task_created"}}); err != nil {
		t.Fatal(err)
	}
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pstore.Close() })
	if err := pstore.CreateSuperuser("boss@x.com", "test"); err != nil {
		t.Fatal(err)
	}

	vf := &auth.Verifier{}
	vf.EnableDelegation([]byte("test-delegation-key"))
	tok, err := vf.MintDelegation("boss@x.com", "boss@x.com/child", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(c, nil, Options{Recordings: rec, Principals: pstore})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	handler := sdkauth.RequireBearerToken(vf.VerifyToken, nil)(mcpHandler)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "delegated-subagent", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: bearerRT{token: tok}},
	}, nil)
	if err != nil {
		t.Fatalf("connect delegated subagent: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "share_recording",
		Arguments: map[string]any{"slug": "golden-run", "visibility": "team", "team_id": "core"},
	})
	if err != nil {
		t.Fatalf("share_recording: %v", err)
	}
	if !res.IsError {
		t.Fatal("delegation token must be refused by share_recording human gate")
	}
}

func TestRecordingsListFilteringSmoke(t *testing.T) {
	dir := t.TempDir()
	c, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	rec, err := recordings.Open(filepath.Join(dir, "recordings.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pstore.Close() })
	if err := pstore.CreateSuperuser("boss@x.com", "test"); err != nil {
		t.Fatal(err)
	}
	if err := pstore.AddMember("member@x.com", "test"); err != nil {
		t.Fatal(err)
	}
	rows := []recordings.MissionMeta{
		{Slug: "mine-private", MissionID: 1, Visibility: "private", SharedBy: "member@x.com"},
		{Slug: "other-private", MissionID: 2, Visibility: "private", SharedBy: "other@x.com"},
		{Slug: "team-visible", MissionID: 3, Visibility: "team", TeamID: "core"},
		{Slug: "public-visible", MissionID: 4, Visibility: "public"},
	}
	for _, m := range rows {
		if err := rec.Upsert(m, []recordings.Event{{TS: 1, Kind: "task_created"}}); err != nil {
			t.Fatalf("upsert %s: %v", m.Slug, err)
		}
	}

	const memberToken = "member-token"
	const bossToken = "boss-token"
	verify := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		switch token {
		case memberToken:
			return &sdkauth.TokenInfo{UserID: "member@x.com", Expiration: time.Now().Add(time.Hour)}, nil
		case bossToken:
			return &sdkauth.TokenInfo{UserID: "boss@x.com", Expiration: time.Now().Add(time.Hour)}, nil
		default:
			return nil, sdkauth.ErrInvalidToken
		}
	}
	srv := NewServer(c, nil, Options{Recordings: rec, Principals: pstore})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	handler := sdkauth.RequireBearerToken(verify, nil)(mcpHandler)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	connect := func(token, name string) *mcp.ClientSession {
		t.Helper()
		client := mcp.NewClient(&mcp.Implementation{Name: name, Version: "0"}, nil)
		sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:   ts.URL,
			HTTPClient: &http.Client{Transport: bearerRT{token: token}},
		}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", name, err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return sess
	}
	memberSess := connect(memberToken, "member")
	bossSess := connect(bossToken, "boss")

	var memberOut listRecordingsOut
	callTask(t, memberSess, "list_recordings", map[string]any{"limit": 20}, &memberOut)
	if len(memberOut.Recordings) != 3 {
		t.Fatalf("member should see 3 recordings (own private + team + public), got %d: %+v", len(memberOut.Recordings), memberOut.Recordings)
	}
	var bossOut listRecordingsOut
	callTask(t, bossSess, "list_recordings", map[string]any{"limit": 20}, &bossOut)
	if len(bossOut.Recordings) != 4 {
		t.Fatalf("superuser should see all recordings, got %d: %+v", len(bossOut.Recordings), bossOut.Recordings)
	}

	denyReplay, err := memberSess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_recording_replay",
		Arguments: map[string]any{"slug": "other-private"},
	})
	if err != nil {
		t.Fatalf("member get_recording_replay: %v", err)
	}
	if !denyReplay.IsError {
		t.Fatal("member should not access another user's private replay")
	}
}
