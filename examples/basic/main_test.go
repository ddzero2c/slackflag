package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ddzero2c/slackflag/internal/mockslack"
)

func TestExampleSlashDeleteUserPreview(t *testing.T) {
	ms := mockslack.New(t)
	srv := newServer("test-secret", "xoxb-test", ms.URL())

	req := mockslack.SignedSlashRequest(t, "test-secret", "/delete-user",
		"-id u123", "U1", "alice", "C1", ms.ResponseURL())
	req.URL.Path = "/slack/command"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	calls := ms.WaitFor(1, time.Second)
	body := calls[0].Body
	if body["response_type"] != "in_channel" {
		t.Fatalf("response_type: %v", body["response_type"])
	}
	blocks := body["blocks"].([]any)
	rendered := ""
	for _, b := range blocks {
		if bm, ok := b.(map[string]any); ok {
			if t, ok := bm["text"].(map[string]any); ok {
				if s, ok := t["text"].(string); ok {
					rendered += s
				}
			}
		}
	}
	if !strings.Contains(rendered, "u123") {
		t.Fatalf("expected u123 in preview, got: %s", rendered)
	}
}

func TestExampleSlashUserStatsDirect(t *testing.T) {
	ms := mockslack.New(t)
	srv := newServer("test-secret", "xoxb-test", ms.URL())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/user-stats",
		"-team eng", "U1", "alice", "C1", ms.ResponseURL())
	req.URL.Path = "/slack/command"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	body := calls[0].Body
	if body["response_type"] != "in_channel" {
		t.Fatalf("response_type: %v", body["response_type"])
	}
}

func TestExampleAdminHelp(t *testing.T) {
	ms := mockslack.New(t)
	srv := newServer("test-secret", "xoxb-test", ms.URL())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/admin",
		"", "U1", "alice", "C1", ms.ResponseURL())
	req.URL.Path = "/slack/command"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("expected ephemeral help for /admin, got %v", calls[0].Body["response_type"])
	}
	rendered := renderText(calls[0].Body["blocks"].([]any))
	if !strings.Contains(rendered, "user-create") || !strings.Contains(rendered, "feature-flag") {
		t.Fatalf("expected sub list in help, got: %s", rendered)
	}
}

func TestExampleAdminFeatureFlagDirect(t *testing.T) {
	ms := mockslack.New(t)
	srv := newServer("test-secret", "xoxb-test", ms.URL())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/admin",
		"feature-flag -name=new-checkout -on", "U1", "alice", "C1", ms.ResponseURL())
	req.URL.Path = "/slack/command"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	body := calls[0].Body
	if body["response_type"] != "in_channel" {
		t.Fatalf("response_type: %v", body["response_type"])
	}
	rendered := renderText(body["blocks"].([]any))
	if !strings.Contains(rendered, "feature new-checkout set on") {
		t.Fatalf("expected subcommand output, got: %s", rendered)
	}
}

func TestExampleAdminUnknownSub(t *testing.T) {
	ms := mockslack.New(t)
	srv := newServer("test-secret", "xoxb-test", ms.URL())
	req := mockslack.SignedSlashRequest(t, "test-secret", "/admin",
		"bogus", "U1", "alice", "C1", ms.ResponseURL())
	req.URL.Path = "/slack/command"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	calls := ms.WaitFor(1, time.Second)
	if calls[0].Body["response_type"] != "ephemeral" {
		t.Fatalf("expected ephemeral, got %v", calls[0].Body["response_type"])
	}
	rendered := renderText(calls[0].Body["blocks"].([]any))
	if !strings.Contains(rendered, "unknown subcommand") {
		t.Fatalf("expected unknown-sub error, got: %s", rendered)
	}
}

func renderText(blocks []any) string {
	var sb strings.Builder
	for _, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := bm["text"].(map[string]any); ok {
			if s, ok := t["text"].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}
