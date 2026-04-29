package slackflag

import (
	"flag"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestMetadataRoundTrip(t *testing.T) {
	fs := flag.NewFlagSet("/test", flag.ContinueOnError)
	id := fs.String("id", "", "")
	n := fs.Int("n", 5, "")
	force := fs.Bool("force", false, "")
	if err := fs.Parse([]string{"-id=u1", "-force"}); err != nil {
		t.Fatal(err)
	}
	_ = id
	_ = n
	_ = force

	m := encodeMetadata(fs, "U001", "2026-04-29T10:00:00Z")
	if m.Args["id"] != "u1" || m.Args["n"] != "5" || m.Args["force"] != "true" {
		t.Fatalf("Args: %#v", m.Args)
	}
	got := append([]string(nil), m.Set...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"force", "id"}) {
		t.Fatalf("Set: %v", m.Set)
	}
	if m.Invoker != "U001" || m.InvokedAt != "2026-04-29T10:00:00Z" {
		t.Fatalf("invoker fields: %#v", m)
	}

	fs2 := flag.NewFlagSet("/test", flag.ContinueOnError)
	id2 := fs2.String("id", "", "")
	n2 := fs2.Int("n", 5, "")
	force2 := fs2.Bool("force", false, "")
	if err := replayMetadata(fs2, m); err != nil {
		t.Fatal(err)
	}
	if *id2 != "u1" {
		t.Fatalf("id: %q", *id2)
	}
	if *n2 != 5 {
		t.Fatalf("n: %d", *n2)
	}
	if !*force2 {
		t.Fatal("force not replayed")
	}
}

func TestMetadataDriftFlagRemoved(t *testing.T) {
	m := metadata{Args: map[string]string{"old": "v"}, Set: []string{"old"}}
	fs := flag.NewFlagSet("/test", flag.ContinueOnError)
	fs.String("new", "", "")
	err := replayMetadata(fs, m)
	if err == nil || !strings.Contains(err.Error(), "no longer defined") {
		t.Fatalf("got %v", err)
	}
}

func TestMetadataMarshalRoundTrip(t *testing.T) {
	m := metadata{
		Args:      map[string]string{"id": "u1"},
		Set:       []string{"id"},
		Invoker:   "U1",
		InvokedAt: "2026-04-29T10:00:00Z",
	}
	raw, err := m.marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, got) {
		t.Fatalf("got %#v want %#v", got, m)
	}
}
