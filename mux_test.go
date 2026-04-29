package slackflag

import (
	"context"
	"flag"
	"net/http/httptest"
	"testing"
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
