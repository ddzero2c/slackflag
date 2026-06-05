package slackflag

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ddzero2c/slackflag/internal/mockslack"
)

func TestPosterPost(t *testing.T) {
	ms := mockslack.New(t)
	p := NewPoster(Config{BotToken: "xoxb-test", SlackBaseURL: ms.URL()})

	ts, err := p.Post(context.Background(), Message{
		Channel:  "C1",
		Fallback: "you have a new order",
		Blocks:   []Block{Header("Order"), Section("body text")},
		Metadata: map[string]any{"order_id": "o-123"},
	})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if ts != "1717000000.000100" {
		t.Fatalf("ts: %q", ts)
	}

	calls := ms.WaitFor(1, time.Second)
	var post *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "chat.postMessage") {
			post = &calls[i]
		}
	}
	if post == nil {
		t.Fatal("no chat.postMessage call recorded")
	}
	if post.Body["channel"] != "C1" {
		t.Fatalf("channel: %v", post.Body["channel"])
	}
	if post.Body["text"] != "you have a new order" {
		t.Fatalf("text(fallback): %v", post.Body["text"])
	}
	if _, ok := post.Body["thread_ts"]; ok {
		t.Fatalf("top-level post must not carry thread_ts")
	}
	blocks, ok := post.Body["blocks"].([]any)
	if !ok {
		t.Fatalf("blocks missing or wrong type: %T", post.Body["blocks"])
	}
	rendered := flattenBlocksText(blocks)
	if !strings.Contains(rendered, "Order") || !strings.Contains(rendered, "body text") {
		t.Fatalf("blocks text: %s", rendered)
	}
	md, ok := post.Body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing or wrong type: %T", post.Body["metadata"])
	}
	if md["event_type"] != metadataEventType {
		t.Fatalf("event_type: %v", md["event_type"])
	}
	ep, ok := md["event_payload"].(map[string]any)
	if !ok || ep["order_id"] != "o-123" {
		t.Fatalf("event_payload: %v", md["event_payload"])
	}
}

func TestPosterPostWithoutMetadata(t *testing.T) {
	ms := mockslack.New(t)
	p := NewPoster(Config{BotToken: "xoxb-test", SlackBaseURL: ms.URL()})

	if _, err := p.Post(context.Background(), Message{
		Channel:  "C1",
		Fallback: "plain",
		Blocks:   []Block{Section("hi")},
	}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	calls := ms.WaitFor(1, time.Second)
	if _, ok := calls[0].Body["metadata"]; ok {
		t.Fatalf("metadata should be absent when Message.Metadata is nil, got %v", calls[0].Body["metadata"])
	}
}
