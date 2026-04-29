package slackflag

import (
	"context"
	"flag"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ddzero2c/slackflag/internal/mockslack"
)

func TestNewMuxPanicsOnEmptySecret(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	NewMux(Config{BotToken: "xoxb-x"})
}

func TestNewMuxPanicsOnEmptyToken(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	NewMux(Config{SigningSecret: "s"})
}

func TestRegisterValidatesExecute(t *testing.T) {
	m := NewMux(Config{SigningSecret: "s", BotToken: "xoxb-x"})
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Preview: func(ctx context.Context, w Response) {}}
	})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil Execute")
		}
	}()
	m.Register(cmd)
}

func TestRegisterDuplicatePanics(t *testing.T) {
	m := NewMux(Config{SigningSecret: "s", BotToken: "xoxb-x"})
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	})
	m.Register(cmd)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate")
		}
	}()
	m.Register(cmd)
}

func TestSlashHandlerRejectsBadSignature(t *testing.T) {
	m := NewMux(Config{SigningSecret: "s", BotToken: "xoxb-x"})
	req := httptest.NewRequest("POST", "/slack/command", nil)
	req.Header.Set("X-Slack-Request-Timestamp", "0")
	req.Header.Set("X-Slack-Signature", "v0=00")
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("got %d", w.Code)
	}
}

// helper used by following tests
func newMuxWithMock(t *testing.T, cmds ...*Command) (*Mux, *mockslack.Server) {
	t.Helper()
	ms := mockslack.New(t)
	m := NewMux(Config{
		SigningSecret: "test-secret",
		BotToken:      "xoxb-test",
		SlackBaseURL:  ms.URL(),
	})
	for _, c := range cmds {
		m.Register(c)
	}
	return m, ms
}

func TestSlashUnknownCommand(t *testing.T) {
	m, ms := newMuxWithMock(t)
	req := mockslack.SignedSlashRequest(t, "test-secret", "/missing", "", "U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	calls := ms.WaitFor(1, time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("response_type: %v", calls[0].Body["response_type"])
	}
}

func TestSlashParseError(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		fs.String("id", "", "user id")
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	})
	m, ms := newMuxWithMock(t, cmd)
	req := mockslack.SignedSlashRequest(t, "test-secret", "/foo", "-bogus", "U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("expected ephemeral, got %v", calls[0].Body["response_type"])
	}
	blocks, _ := calls[0].Body["blocks"].([]any)
	rendered := flattenBlocksText(blocks)
	if !strings.Contains(rendered, "Usage") && !strings.Contains(rendered, "flag provided") {
		t.Fatalf("expected usage info, got: %s", rendered)
	}
}

func TestSlashHelpFlag(t *testing.T) {
	cmd := New("/foo", "do foo", func(fs *flag.FlagSet) Handlers {
		fs.String("id", "", "user id")
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	})
	m, ms := newMuxWithMock(t, cmd)
	req := mockslack.SignedSlashRequest(t, "test-secret", "/foo", "-h", "U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	rendered := flattenBlocksText(calls[0].Body["blocks"].([]any))
	if !strings.Contains(rendered, "-id") {
		t.Fatalf("expected flag listing, got: %s", rendered)
	}
}

func TestSlashPreviewFail(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Preview: func(ctx context.Context, w Response) { w.Fail(errFakeNotFound) },
			Execute: func(ctx context.Context, w Response) {},
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	req := mockslack.SignedSlashRequest(t, "test-secret", "/foo", "", "U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("expected ephemeral on Fail, got %v", calls[0].Body["response_type"])
	}
}

func TestSlashPreviewSuccess(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		id := fs.String("id", "", "")
		return Handlers{
			Preview: func(ctx context.Context, w Response) {
				fmt.Fprintf(w, "preview id=%s", *id)
			},
			Execute: func(ctx context.Context, w Response) {},
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	req := mockslack.SignedSlashRequest(t, "test-secret", "/foo", "-id u1", "U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	body := calls[0].Body
	if body["response_type"] != "in_channel" {
		t.Fatalf("expected in_channel, got %v", body["response_type"])
	}
	blocks := body["blocks"].([]any)
	rendered := flattenBlocksText(blocks)
	if !strings.Contains(rendered, "preview id=u1") {
		t.Fatalf("preview text missing: %s", rendered)
	}
	// Last block should be actions with Confirm + Cancel
	last := blocks[len(blocks)-1].(map[string]any)
	if last["type"] != "actions" {
		t.Fatalf("last block not actions: %v", last)
	}
	// metadata should be present and decode
	md := body["metadata"].(map[string]any)
	if md["event_type"] != "slackflag" {
		t.Fatalf("event_type: %v", md["event_type"])
	}
	payload := md["event_payload"].(map[string]any)
	args := payload["args"].(map[string]any)
	if args["id"] != "u1" {
		t.Fatalf("args.id: %v", args["id"])
	}
	if payload["invoker"] != "U1" {
		t.Fatalf("invoker: %v", payload["invoker"])
	}
}

func TestSlashDirectFlow(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		id := fs.String("id", "", "")
		return Handlers{
			Execute: func(ctx context.Context, w Response) {
				fmt.Fprintf(w, "ran for %s", *id)
			},
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	req := mockslack.SignedSlashRequest(t, "test-secret", "/foo", "-id u1", "U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	body := calls[0].Body
	if body["response_type"] != "in_channel" {
		t.Fatalf("expected in_channel, got %v", body["response_type"])
	}
	rendered := flattenBlocksText(body["blocks"].([]any))
	if !strings.Contains(rendered, "ran for u1") {
		t.Fatalf("output missing: %s", rendered)
	}
	if !strings.Contains(rendered, "ran by <@alice>") {
		t.Fatalf("footer missing: %s", rendered)
	}
}

func TestSlashDirectFlowFail(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Execute: func(ctx context.Context, w Response) { w.Fail(fmt.Errorf("nope")) },
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	req := mockslack.SignedSlashRequest(t, "test-secret", "/foo", "", "U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	body := calls[0].Body
	if body["response_type"] != "in_channel" {
		t.Fatalf("direct flow Fail should still go in_channel for audit, got %v", body["response_type"])
	}
	rendered := flattenBlocksText(body["blocks"].([]any))
	if !strings.Contains(rendered, ":x:") {
		t.Fatalf("expected ❌ marker: %s", rendered)
	}
}

// flattenBlocksText extracts visible text from a Slack blocks payload (test helper).
func flattenBlocksText(blocks []any) string {
	var sb strings.Builder
	for _, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := bm["text"].(map[string]any); ok {
			if s, ok := t["text"].(string); ok {
				sb.WriteString(s)
				sb.WriteString("\n")
			}
		}
		if fields, ok := bm["fields"].([]any); ok {
			for _, f := range fields {
				if fm, ok := f.(map[string]any); ok {
					if s, ok := fm["text"].(string); ok {
						sb.WriteString(s)
						sb.WriteString("\n")
					}
				}
			}
		}
	}
	return sb.String()
}

// errFakeNotFound is a stable error for tests asserting on Fail behavior.
var errFakeNotFound = fmt.Errorf("user not found")
