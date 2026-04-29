package slackflag

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSection(t *testing.T) {
	b, err := json.Marshal(Section("hello *world*"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"type":"section"`) ||
		!strings.Contains(s, `"type":"mrkdwn"`) ||
		!strings.Contains(s, `"text":"hello *world*"`) {
		t.Fatalf("unexpected block: %s", s)
	}
}

func TestHeader(t *testing.T) {
	b, _ := json.Marshal(Header("Title"))
	s := string(b)
	if !strings.Contains(s, `"type":"header"`) ||
		!strings.Contains(s, `"type":"plain_text"`) ||
		!strings.Contains(s, `"text":"Title"`) {
		t.Fatalf("unexpected block: %s", s)
	}
}

func TestFields(t *testing.T) {
	b, _ := json.Marshal(Fields("ID", "u1", "Force", "true"))
	s := string(b)
	if !strings.Contains(s, `*ID*\nu1`) || !strings.Contains(s, `*Force*\ntrue`) {
		t.Fatalf("unexpected block: %s", s)
	}
}

func TestFieldsPanicOnOdd(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on odd args")
		}
	}()
	Fields("only-key")
}

func TestDivider(t *testing.T) {
	b, _ := json.Marshal(Divider())
	if string(b) != `{"type":"divider"}` {
		t.Fatalf("got %s", b)
	}
}
