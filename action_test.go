package slackflag

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ddzero2c/slackflag/internal/mockslack"
)

// actionPayload builds a block_actions payload mirroring a click on a button
// that slackflag posted proactively (via Poster). The message carries a single
// actions block plus an optional metadata.event_payload.
func actionPayload(respURL, actionID, value, userID, userName string, eventPayload map[string]any) map[string]any {
	msg := map[string]any{
		"ts": "1717000000.000100",
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "New order"}},
			map[string]any{"type": "actions", "elements": []any{
				map[string]any{
					"type":      "button",
					"text":      map[string]any{"type": "plain_text", "text": "Approve"},
					"action_id": actionID,
					"value":     value,
				},
			}},
		},
	}
	if eventPayload != nil {
		msg["metadata"] = map[string]any{"event_type": metadataEventType, "event_payload": eventPayload}
	}
	return map[string]any{
		"type":    "block_actions",
		"user":    map[string]any{"id": userID, "name": userName},
		"channel": map[string]any{"id": "C1"},
		"actions": []any{
			map[string]any{"action_id": actionID, "value": value},
		},
		"message":      msg,
		"response_url": respURL,
	}
}

// hasActionsBlock reports whether a decoded blocks payload contains an actions
// block, reusing the same detection stripActions applies.
func hasActionsBlock(blocks []any) bool {
	return len(stripActions(blocks)) != len(blocks)
}

func TestHandleActionHappyPath(t *testing.T) {
	m, ms := newMuxWithMock(t)
	var gotIC Interaction
	m.HandleAction("order.approve", func(ctx context.Context, ic Interaction, w Response) error {
		gotIC = ic
		fmt.Fprintf(w, "approved %s by <@%s>", ic.Value, ic.UserName)
		return nil
	})

	payload := actionPayload(ms.ResponseURL(), "order.approve", "o-7", "U9", "carol",
		map[string]any{"order_id": "o-7"})
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}

	calls := ms.WaitFor(2, 2*time.Second)
	var processing, final *mockslack.Call
	for i := range calls {
		if !strings.Contains(calls[i].URL, "/response/") || calls[i].Body["replace_original"] != true {
			continue
		}
		text := flattenBlocksText(calls[i].Body["blocks"].([]any))
		switch {
		case strings.Contains(text, "running by <@carol>"):
			processing = &calls[i]
		case strings.Contains(text, "approved o-7 by <@carol>"):
			final = &calls[i]
		}
	}
	if processing == nil {
		t.Fatalf("missing markProcessing replace_original; calls=%v", calls)
	}
	if hasActionsBlock(processing.Body["blocks"].([]any)) {
		t.Fatal("buttons must be stripped while processing")
	}
	if final == nil {
		t.Fatalf("missing replace_original with handler blocks; calls=%v", calls)
	}
	if hasActionsBlock(final.Body["blocks"].([]any)) {
		t.Fatal("final message should not carry the original buttons")
	}

	if gotIC.ActionID != "order.approve" || gotIC.Value != "o-7" {
		t.Fatalf("action/value: %+v", gotIC)
	}
	if gotIC.UserID != "U9" || gotIC.UserName != "carol" || gotIC.Channel != "C1" {
		t.Fatalf("user/channel fields: %+v", gotIC)
	}
	if gotIC.MessageTS != "1717000000.000100" {
		t.Fatalf("message ts: %q", gotIC.MessageTS)
	}
	if gotIC.Metadata["order_id"] != "o-7" {
		t.Fatalf("metadata not passed through: %v", gotIC.Metadata)
	}
	if gotIC.Raw == nil || gotIC.Raw.User.ID != "U9" {
		t.Fatalf("Raw slack-go callback escape hatch not populated: %+v", gotIC.Raw)
	}
}

func TestHandleActionFailRestoresButtons(t *testing.T) {
	m, ms := newMuxWithMock(t)
	m.HandleAction("order.approve", func(ctx context.Context, ic Interaction, w Response) error {
		return fmt.Errorf("downstream unavailable")
	})

	payload := actionPayload(ms.ResponseURL(), "order.approve", "o-7", "U9", "carol", nil)
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)

	calls := ms.WaitFor(2, 2*time.Second)
	var restore *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "/response/") &&
			calls[i].Body["replace_original"] == true &&
			hasActionsBlock(calls[i].Body["blocks"].([]any)) {
			restore = &calls[i]
		}
	}
	if restore == nil {
		t.Fatal("expected restore with buttons intact after failure so user can retry")
	}
	if !strings.Contains(flattenBlocksText(restore.Body["blocks"].([]any)), "downstream unavailable") {
		t.Fatalf("error not surfaced: %v", restore.Body)
	}
}

func TestHandleActionPanicRecovers(t *testing.T) {
	m, ms := newMuxWithMock(t)
	m.HandleAction("order.approve", func(ctx context.Context, ic Interaction, w Response) error {
		panic("boom")
	})

	payload := actionPayload(ms.ResponseURL(), "order.approve", "o-7", "U9", "carol", nil)
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req) // must not crash

	calls := ms.WaitFor(2, 2*time.Second)
	var restore *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "/response/") &&
			calls[i].Body["replace_original"] == true &&
			hasActionsBlock(calls[i].Body["blocks"].([]any)) {
			restore = &calls[i]
		}
	}
	if restore == nil {
		t.Fatal("expected restore after panic")
	}
	if !strings.Contains(flattenBlocksText(restore.Body["blocks"].([]any)), "panic: boom") {
		t.Fatalf("panic not surfaced: %v", restore.Body)
	}
}

func TestHandleActionReservedPrefixPanics(t *testing.T) {
	m := NewMux(Config{SigningSecret: "s", BotToken: "xoxb-x"})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on reserved slackflag. prefix")
		}
	}()
	m.HandleAction("slackflag.foo", func(ctx context.Context, ic Interaction, w Response) error { return nil })
}

func TestHandleActionDuplicatePanics(t *testing.T) {
	m := NewMux(Config{SigningSecret: "s", BotToken: "xoxb-x"})
	h := func(ctx context.Context, ic Interaction, w Response) error { return nil }
	m.HandleAction("order.approve", h)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate action_id")
		}
	}()
	m.HandleAction("order.approve", h)
}

func TestHandleActionChaining(t *testing.T) {
	m := NewMux(Config{SigningSecret: "s", BotToken: "xoxb-x"})
	h := func(ctx context.Context, ic Interaction, w Response) error { return nil }
	if got := m.HandleAction("a", h).HandleAction("b", h); got != m {
		t.Fatal("HandleAction must return the Mux for chaining")
	}
}

func TestUnknownActionIsNoOp(t *testing.T) {
	m, ms := newMuxWithMock(t)
	payload := actionPayload(ms.ResponseURL(), "unregistered.action", "v", "U1", "alice", nil)
	req := mockslack.SignedInteractionRequest(t, "test-secret", payload)
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	time.Sleep(150 * time.Millisecond)
	if got := ms.Calls(); len(got) != 0 {
		t.Fatalf("expected no Slack calls for unknown action, got %d: %v", len(got), got)
	}
}

func TestInteractionRejectsBadSignature(t *testing.T) {
	m, ms := newMuxWithMock(t)
	ran := false
	m.HandleAction("order.approve", func(ctx context.Context, ic Interaction, w Response) error {
		ran = true
		return nil
	})
	req := httptest.NewRequest("POST", "/slack/interact",
		strings.NewReader("payload=%7B%7D"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", "0")
	req.Header.Set("X-Slack-Signature", "v0=00")
	w := httptest.NewRecorder()
	m.InteractionHandler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if ran {
		t.Fatal("handler must not run when signature is invalid")
	}
	_ = ms
}
