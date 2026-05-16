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
	if n := strings.Count(rendered, "Usage of"); n != 1 {
		t.Fatalf("expected usage header once, got %d:\n%s", n, rendered)
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

func TestInteractionConfirmSuccess(t *testing.T) {
	cmd := New("/delete-user", "", func(fs *flag.FlagSet) Handlers {
		id := fs.String("id", "", "")
		return Handlers{
			Preview: func(ctx context.Context, w Response) { fmt.Fprintf(w, "preview %s", *id) },
			Execute: func(ctx context.Context, w Response) {
				fmt.Fprintf(w, "deleted %s", *id)
			},
		}
	})
	m, ms := newMuxWithMock(t, cmd)

	payload := map[string]any{
		"type": "block_actions",
		"user": map[string]any{"id": "U1", "name": "alice"},
		"channel": map[string]any{"id": "C1"},
		"actions": []any{
			map[string]any{"action_id": "slackflag.confirm", "value": "/delete-user"},
		},
		"message": map[string]any{
			"ts":     "1714000000.001",
			"blocks": []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "preview u1"}}, map[string]any{"type": "actions"}},
			"metadata": map[string]any{
				"event_type": "slackflag",
				"event_payload": map[string]any{
					"args":       map[string]any{"id": "u1"},
					"set":        []any{"id"},
					"invoker":    "U1",
					"invoked_at": "2026-04-29T10:00:00Z",
				},
			},
		},
		"response_url": ms.ResponseURL(),
	}
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	// expect: markProcessing + thread post + finalize replace_original
	calls := ms.WaitFor(3, 2*time.Second)

	var threadCall, processingCall, finalizeCall *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "chat.postMessage") {
			threadCall = &calls[i]
			continue
		}
		if strings.Contains(calls[i].URL, "/response/") && calls[i].Body["replace_original"] == true {
			text := flattenBlocksText(calls[i].Body["blocks"].([]any))
			switch {
			case strings.Contains(text, "running by"):
				processingCall = &calls[i]
			case strings.Contains(text, "executed by"):
				finalizeCall = &calls[i]
			}
		}
	}
	if processingCall == nil {
		t.Fatalf("missing markProcessing replace_original; calls=%v", calls)
	}
	if threadCall == nil || finalizeCall == nil {
		t.Fatalf("missing call: thread=%v finalize=%v calls=%v", threadCall, finalizeCall, calls)
	}
	if threadCall.Body["thread_ts"] != "1714000000.001" {
		t.Fatalf("thread_ts: %v", threadCall.Body["thread_ts"])
	}
	threadText := flattenBlocksText(threadCall.Body["blocks"].([]any))
	if !strings.Contains(threadText, "deleted u1") {
		t.Fatalf("thread reply: %s", threadText)
	}

	finalizeText := flattenBlocksText(finalizeCall.Body["blocks"].([]any))
	if strings.Contains(finalizeText, "Confirm") || strings.Contains(finalizeText, "Cancel") {
		t.Fatalf("buttons should be stripped: %s", finalizeText)
	}
	if !strings.Contains(finalizeText, "executed by <@alice>") {
		t.Fatalf("footer missing: %s", finalizeText)
	}
	// processing message must also have actions stripped
	for _, b := range processingCall.Body["blocks"].([]any) {
		if bm, ok := b.(map[string]any); ok && bm["type"] == "actions" {
			t.Fatal("actions block must be stripped during processing")
		}
	}
}

// errFakeNotFound is a stable error for tests asserting on Fail behavior.
var errFakeNotFound = fmt.Errorf("user not found")

// TestInteractionConfirmDisablesButtonsBeforeExecute pins the timing contract:
// the markProcessing replace_original (which strips the actions block) must hit
// Slack *before* Execute returns, so the invoker can't double-click during a
// slow Execute.
func TestInteractionConfirmDisablesButtonsBeforeExecute(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	cmd := New("/delete-user", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Preview: func(ctx context.Context, w Response) {},
			Execute: func(ctx context.Context, w Response) {
				close(started)
				<-release
				fmt.Fprint(w, "done")
			},
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	payload := basicConfirmPayload(ms.ResponseURL(), "/delete-user", "U1", "alice", map[string]any{
		"args": map[string]any{}, "set": []any{}, "invoker": "U1", "invoked_at": "2026-04-29T10:00:00Z",
	})
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute never ran")
	}
	// while Execute is blocked: markProcessing must already be visible.
	calls := ms.WaitFor(1, 2*time.Second)
	var mark *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "/response/") && calls[i].Body["replace_original"] == true {
			mark = &calls[i]
		}
	}
	if mark == nil {
		t.Fatal("markProcessing must reach Slack before Execute returns")
	}
	rendered := flattenBlocksText(mark.Body["blocks"].([]any))
	if !strings.Contains(rendered, "running by <@alice>") {
		t.Fatalf("processing footer missing: %s", rendered)
	}
	for _, b := range mark.Body["blocks"].([]any) {
		if bm, ok := b.(map[string]any); ok && bm["type"] == "actions" {
			t.Fatal("actions block must be stripped while processing")
		}
	}
	close(release)
}

func TestInteractionExecuteFailPreservesButton(t *testing.T) {
	cmd := New("/delete-user", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Preview: func(ctx context.Context, w Response) {},
			Execute: func(ctx context.Context, w Response) { w.Fail(fmt.Errorf("db down")) },
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	payload := basicConfirmPayload(ms.ResponseURL(), "/delete-user", "U1", "alice", map[string]any{
		"args": map[string]any{}, "set": []any{}, "invoker": "U1", "invoked_at": "2026-04-29T10:00:00Z",
	})
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	// expect: markProcessing + thread reply + restoreButtons
	calls := ms.WaitFor(3, 2*time.Second)

	var thread, processing, restore *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "chat.postMessage") {
			thread = &calls[i]
			continue
		}
		if !strings.Contains(calls[i].URL, "/response/") || calls[i].Body["replace_original"] != true {
			continue
		}
		// distinguish processing (no actions block) from restore (actions intact)
		hasActions := false
		for _, b := range calls[i].Body["blocks"].([]any) {
			if bm, ok := b.(map[string]any); ok && bm["type"] == "actions" {
				hasActions = true
				break
			}
		}
		if hasActions {
			restore = &calls[i]
		} else {
			processing = &calls[i]
		}
	}
	if processing == nil {
		t.Fatal("expected markProcessing replace_original before Execute")
	}
	if restore == nil {
		t.Fatal("expected restoreButtons replace_original (actions intact) after failure so user can retry")
	}
	if thread == nil {
		t.Fatal("missing thread reply")
	}
	if !strings.Contains(flattenBlocksText(thread.Body["blocks"].([]any)), "db down") {
		t.Fatalf("err missing in thread: %v", thread.Body)
	}
}

func TestInteractionExecutePanicRecovers(t *testing.T) {
	cmd := New("/delete-user", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Preview: func(ctx context.Context, w Response) {},
			Execute: func(ctx context.Context, w Response) { panic("oops") },
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	payload := basicConfirmPayload(ms.ResponseURL(), "/delete-user", "U1", "alice", map[string]any{
		"args": map[string]any{}, "set": []any{}, "invoker": "U1", "invoked_at": "2026-04-29T10:00:00Z",
	})
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req) // must not crash
	// panic is treated as failure: markProcessing + thread reply + restoreButtons
	calls := ms.WaitFor(3, 2*time.Second)
	var thread *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "chat.postMessage") {
			thread = &calls[i]
		}
	}
	if thread == nil {
		t.Fatal("expected thread reply on panic")
	}
	if !strings.Contains(flattenBlocksText(thread.Body["blocks"].([]any)), "panic: oops") {
		t.Fatalf("panic err missing: %v", thread.Body)
	}
}

func TestInteractionConfirmByNonInvoker(t *testing.T) {
	cmd := New("/delete-user", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Preview: func(ctx context.Context, w Response) {},
			Execute: func(ctx context.Context, w Response) { fmt.Fprint(w, "should not run") },
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	payload := basicConfirmPayload(ms.ResponseURL(), "/delete-user", "U2", "bob", map[string]any{
		"args": map[string]any{}, "set": []any{}, "invoker": "U1", "invoked_at": "2026-04-29T10:00:00Z",
	})
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, 2*time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("expected ephemeral lock msg")
	}
	for _, c := range calls {
		if strings.Contains(c.URL, "chat.postMessage") {
			t.Fatalf("Execute must not run for non-invoker")
		}
	}
}

func TestInteractionCancel(t *testing.T) {
	cmd := New("/delete-user", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Preview: func(ctx context.Context, w Response) {},
			Execute: func(ctx context.Context, w Response) { fmt.Fprint(w, "should not run") },
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	payload := basicConfirmPayload(ms.ResponseURL(), "/delete-user", "U1", "alice", map[string]any{
		"args": map[string]any{}, "set": []any{}, "invoker": "U1", "invoked_at": "2026-04-29T10:00:00Z",
	})
	payload["actions"] = []any{
		map[string]any{"action_id": "slackflag.cancel", "value": "/delete-user"},
	}
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, 2*time.Second)
	var update *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "/response/") && calls[i].Body["replace_original"] == true {
			update = &calls[i]
		}
	}
	if update == nil {
		t.Fatal("expected replace_original update on cancel")
	}
	if !strings.Contains(flattenBlocksText(update.Body["blocks"].([]any)), "cancelled by <@alice>") {
		t.Fatalf("cancel footer missing: %v", update.Body)
	}
}

// basicConfirmPayload builds a minimal block_actions payload mirroring what
// Slack would POST when a user clicks Confirm on a slackflag preview.
func basicConfirmPayload(respURL, cmdName, userID, userName string, eventPayload map[string]any) map[string]any {
	return map[string]any{
		"type":    "block_actions",
		"user":    map[string]any{"id": userID, "name": userName},
		"channel": map[string]any{"id": "C1"},
		"actions": []any{
			map[string]any{"action_id": "slackflag.confirm", "value": cmdName},
		},
		"message": map[string]any{
			"ts": "1714000000.001",
			"blocks": []any{
				map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "preview"}},
				map[string]any{"type": "actions"},
			},
			"metadata": map[string]any{
				"event_type":    "slackflag",
				"event_payload": eventPayload,
			},
		},
		"response_url": respURL,
	}
}

func TestSlashValidateFail(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Validate: func() error { return fmt.Errorf("id is required") },
			Preview:  func(ctx context.Context, w Response) { t.Fatal("Preview must not run") },
			Execute:  func(ctx context.Context, w Response) { t.Fatal("Execute must not run") },
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	req := mockslack.SignedSlashRequest(t, "test-secret", "/foo", "", "U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("expected ephemeral on validate fail, got %v", calls[0].Body["response_type"])
	}
	rendered := flattenBlocksText(calls[0].Body["blocks"].([]any))
	if !strings.Contains(rendered, "id is required") {
		t.Fatalf("expected error text in blocks, got: %s", rendered)
	}
}

func TestSlashValidatePassDirectFlow(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		id := fs.String("id", "", "")
		return Handlers{
			Validate: func() error { return nil },
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
	if calls[0].Body["response_type"] != "in_channel" {
		t.Fatalf("expected in_channel after validate passes, got %v", calls[0].Body["response_type"])
	}
	if !strings.Contains(flattenBlocksText(calls[0].Body["blocks"].([]any)), "ran for u1") {
		t.Fatalf("expected execute output")
	}
}

func TestInteractionCorruptMetadata(t *testing.T) {
	cmd := New("/delete-user", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Preview: func(ctx context.Context, w Response) {},
			Execute: func(ctx context.Context, w Response) { t.Fatal("Execute must not run on corrupt metadata") },
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	payload := map[string]any{
		"type":    "block_actions",
		"user":    map[string]any{"id": "U1", "name": "alice"},
		"channel": map[string]any{"id": "C1"},
		"actions": []any{
			map[string]any{"action_id": "slackflag.confirm", "value": "/delete-user"},
		},
		"message": map[string]any{
			"ts":     "1714000000.001",
			"blocks": []any{},
			"metadata": map[string]any{
				"event_type":    "slackflag",
				"event_payload": "this is not a valid metadata object",
			},
		},
		"response_url": ms.ResponseURL(),
	}
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, 2*time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("expected ephemeral for corrupt metadata, got %v", calls[0].Body["response_type"])
	}
}

func TestSlashFailureEchoesInput(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Validate: func() error { return fmt.Errorf("id is required") },
			Execute:  func(ctx context.Context, w Response) {},
		}
	})
	m, ms := newMuxWithMock(t, cmd)
	req := mockslack.SignedSlashRequest(t, "test-secret", "/foo", "-bogus value",
		"U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	rendered := flattenBlocksText(calls[0].Body["blocks"].([]any))
	if !strings.Contains(rendered, "/foo -bogus value") {
		t.Fatalf("expected input echo in failure, got: %s", rendered)
	}
}

func TestSubcommandUnknownEchoesInput(t *testing.T) {
	m, ms := newMuxWithMock(t, adminCmd())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/admin", "nonexistent extra",
		"U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	rendered := flattenBlocksText(calls[0].Body["blocks"].([]any))
	if !strings.Contains(rendered, "/admin nonexistent extra") {
		t.Fatalf("expected input echo in unknown-sub failure, got: %s", rendered)
	}
}

func TestRegisterRequiresLeadingSlash(t *testing.T) {
	m := NewMux(Config{SigningSecret: "s", BotToken: "xoxb-x"})
	cmd := New("foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on top-level command without leading /")
		}
	}()
	m.Register(cmd)
}

// adminCmd builds a /admin parent with two subs: user-create (confirm flow)
// and user-stats (direct flow). Used by several subcommand tests.
func adminCmd() *Command {
	parent := New("/admin", "admin tools", nil)
	parent.AddSubcommand(New("user-create", "create a user",
		func(fs *flag.FlagSet) Handlers {
			name := fs.String("name", "", "user name")
			return Handlers{
				Validate: func() error {
					if *name == "" {
						return fmt.Errorf("-name is required")
					}
					return nil
				},
				Preview: func(ctx context.Context, w Response) {
					fmt.Fprintf(w, "about to create %s", *name)
				},
				Execute: func(ctx context.Context, w Response) {
					fmt.Fprintf(w, "created %s", *name)
				},
			}
		}))
	parent.AddSubcommand(New("user-stats", "show user stats (direct)",
		func(fs *flag.FlagSet) Handlers {
			team := fs.String("team", "", "team")
			return Handlers{
				Execute: func(ctx context.Context, w Response) {
					fmt.Fprintf(w, "stats for %s", *team)
				},
			}
		}))
	return parent
}

func TestSubcommandDispatchDirect(t *testing.T) {
	m, ms := newMuxWithMock(t, adminCmd())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/admin", "user-stats -team eng",
		"U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	body := calls[0].Body
	if body["response_type"] != "in_channel" {
		t.Fatalf("response_type: %v", body["response_type"])
	}
	if !strings.Contains(flattenBlocksText(body["blocks"].([]any)), "stats for eng") {
		t.Fatalf("expected sibling sub not invoked; rendered: %s",
			flattenBlocksText(body["blocks"].([]any)))
	}
}

func TestSubcommandDispatchConfirmFlow(t *testing.T) {
	m, ms := newMuxWithMock(t, adminCmd())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/admin", "user-create -name alice",
		"U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	body := calls[0].Body
	if body["response_type"] != "in_channel" {
		t.Fatalf("expected in_channel preview, got %v", body["response_type"])
	}
	rendered := flattenBlocksText(body["blocks"].([]any))
	if !strings.Contains(rendered, "about to create alice") {
		t.Fatalf("preview text missing: %s", rendered)
	}
	md := body["metadata"].(map[string]any)
	payload := md["event_payload"].(map[string]any)
	subField, ok := payload["sub"].([]any)
	if !ok || len(subField) != 1 || subField[0] != "user-create" {
		t.Fatalf("expected sub=[user-create] in metadata, got %#v", payload["sub"])
	}
	args := payload["args"].(map[string]any)
	if args["name"] != "alice" {
		t.Fatalf("args.name: %v", args["name"])
	}
	// Button stores the top-level slash, not the sub.
	last := body["blocks"].([]any)[len(body["blocks"].([]any))-1].(map[string]any)
	if last["type"] != "actions" {
		t.Fatalf("expected actions block")
	}
	for _, el := range last["elements"].([]any) {
		if elm := el.(map[string]any); elm["value"] != "/admin" {
			t.Fatalf("button value should be top-level /admin, got %v", elm["value"])
		}
	}
}

func TestSubcommandHelpRoot(t *testing.T) {
	m, ms := newMuxWithMock(t, adminCmd())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/admin", "",
		"U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("expected ephemeral help, got %v", calls[0].Body["response_type"])
	}
	rendered := flattenBlocksText(calls[0].Body["blocks"].([]any))
	if !strings.Contains(rendered, "user-create") || !strings.Contains(rendered, "user-stats") {
		t.Fatalf("expected sub names in help, got: %s", rendered)
	}
	if !strings.Contains(rendered, "create a user") {
		t.Fatalf("expected sub descriptions, got: %s", rendered)
	}
}

func TestSubcommandHelpFlagAndKeyword(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		t.Run(arg, func(t *testing.T) {
			m, ms := newMuxWithMock(t, adminCmd())
			req := mockslack.SignedSlashRequest(t, "test-secret", "/admin", arg,
				"U1", "alice", "C1", ms.ResponseURL())
			w := httptest.NewRecorder()
			m.SlashHandler().ServeHTTP(w, req)
			calls := ms.WaitFor(1, time.Second)
			rendered := flattenBlocksText(calls[0].Body["blocks"].([]any))
			if !strings.Contains(rendered, "user-create") {
				t.Fatalf("%s: expected sub list, got: %s", arg, rendered)
			}
		})
	}
}

func TestSubcommandLeafHelp(t *testing.T) {
	m, ms := newMuxWithMock(t, adminCmd())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/admin", "user-create -h",
		"U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	rendered := flattenBlocksText(calls[0].Body["blocks"].([]any))
	// Leaf help comes from flag.FlagSet.Usage — should include flag name.
	if !strings.Contains(rendered, "-name") {
		t.Fatalf("expected flag listing for user-create, got: %s", rendered)
	}
}

func TestSubcommandUnknown(t *testing.T) {
	m, ms := newMuxWithMock(t, adminCmd())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/admin", "nonexistent",
		"U1", "alice", "C1", ms.ResponseURL())
	w := httptest.NewRecorder()
	m.SlashHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("expected ephemeral for unknown sub, got %v", calls[0].Body["response_type"])
	}
	rendered := flattenBlocksText(calls[0].Body["blocks"].([]any))
	if !strings.Contains(rendered, "unknown subcommand") {
		t.Fatalf("expected unknown-sub error, got: %s", rendered)
	}
	if !strings.Contains(rendered, "user-create") {
		t.Fatalf("expected sub list in unknown-sub message, got: %s", rendered)
	}
}

func TestSubcommandConfirmRoundTrip(t *testing.T) {
	m, ms := newMuxWithMock(t, adminCmd())
	payload := basicConfirmPayload(ms.ResponseURL(), "/admin", "U1", "alice", map[string]any{
		"args":       map[string]any{"name": "alice"},
		"set":        []any{"name"},
		"sub":        []any{"user-create"},
		"invoker":    "U1",
		"invoked_at": "2026-04-29T10:00:00Z",
	})
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	// markProcessing + thread reply + finalizePreview
	calls := ms.WaitFor(3, 2*time.Second)
	var thread *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "chat.postMessage") {
			thread = &calls[i]
		}
	}
	if thread == nil {
		t.Fatal("expected thread reply")
	}
	threadText := flattenBlocksText(thread.Body["blocks"].([]any))
	if !strings.Contains(threadText, "created alice") {
		t.Fatalf("expected user-create Execute output, got: %s", threadText)
	}
}

func TestSubcommandConfirmDrift(t *testing.T) {
	m, ms := newMuxWithMock(t, adminCmd())
	payload := basicConfirmPayload(ms.ResponseURL(), "/admin", "U1", "alice", map[string]any{
		"args":       map[string]any{},
		"set":        []any{},
		"sub":        []any{"removed-sub"},
		"invoker":    "U1",
		"invoked_at": "2026-04-29T10:00:00Z",
	})
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	calls := ms.WaitFor(1, 2*time.Second)
	// Should have a thread reply with the drift error, no replace_original.
	var thread *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "chat.postMessage") {
			thread = &calls[i]
		}
		if strings.Contains(calls[i].URL, "/response/") && calls[i].Body["replace_original"] == true {
			t.Fatalf("button must not be stripped on drift")
		}
	}
	if thread == nil {
		t.Fatal("expected thread reply with drift error")
	}
	threadText := flattenBlocksText(thread.Body["blocks"].([]any))
	if !strings.Contains(threadText, "no longer exists") {
		t.Fatalf("expected drift error, got: %s", threadText)
	}
}

func TestSlashConcurrentInvocations(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		id := fs.String("id", "", "")
		return Handlers{
			Preview: func(ctx context.Context, w Response) {
				fmt.Fprintf(w, "preview %s", *id)
			},
			Execute: func(ctx context.Context, w Response) {},
		}
	})
	m, ms := newMuxWithMock(t, cmd)

	const N = 20
	done := make(chan struct{}, N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer func() { done <- struct{}{} }()
			req := mockslack.SignedSlashRequest(t, "test-secret", "/foo",
				fmt.Sprintf("-id u%d", i), fmt.Sprintf("U%d", i), "alice", "C1", ms.ResponseURL())
			w := httptest.NewRecorder()
			m.SlashHandler().ServeHTTP(w, req)
		}()
	}
	for i := 0; i < N; i++ {
		<-done
	}
	calls := ms.WaitFor(N, 5*time.Second)
	seen := map[string]bool{}
	for _, c := range calls {
		blocks := c.Body["blocks"].([]any)
		text := flattenBlocksText(blocks)
		// each invocation should see its own id
		for i := 0; i < N; i++ {
			needle := fmt.Sprintf("preview u%d", i)
			if strings.Contains(text, needle) {
				seen[needle] = true
			}
		}
	}
	if len(seen) != N {
		t.Fatalf("expected %d unique previews, got %d", N, len(seen))
	}
}
