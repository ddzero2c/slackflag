# slackflag Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go package that wraps Slack slash commands with a `flag.FlagSet`-driven preview/confirm/execute flow, fully described in `docs/superpowers/specs/2026-04-29-slackflag-design.md`.

**Architecture:** Stateless HTTP middleware. A single `*Mux` registers many `*Command`s and exposes two `http.Handler`s (slash + interaction). Args are serialized into Slack message `metadata.event_payload` between the preview and execute requests. `*Response` is a small writer that handlers fill, then framework code packages it into Slack blocks and posts via `response_url` and `chat.postMessage`.

**Tech Stack:** Go 1.26.1. Stdlib only — `net/http`, `flag`, `encoding/json`, `crypto/hmac`, `crypto/sha256`, `encoding/hex`, `log/slog`. No external deps.

---

## File Structure

```
go.mod                              (already exists)
tokenize.go            tokenize_test.go             — slash text → argv tokenizer
signature.go           signature_test.go            — Slack HMAC verification
block.go               block_test.go                — Block type alias + helpers
response.go            response_test.go             — Response interface + impl
command.go             command_test.go              — Command type, New, validation
metadata.go            metadata_test.go             — FlagSet ↔ message metadata
slackapi.go                                          — internal client (response_url, chat.postMessage)
mux.go                 mux_test.go                  — HTTP routing, dispatch, async runner
internal/mockslack/mockslack.go                      — test helper: capture Slack API calls
examples/basic/main.go                               — runnable example wired to mockslack for smoke test
examples/basic/main_test.go                          — smoke test
```

Each file has one responsibility. Internal helpers stay lowercase. Tests live alongside.

---

## Tasks

### Task 1: Tokenize slash text

**Files:**
- Create: `tokenize.go`
- Create: `tokenize_test.go`

- [ ] **Step 1: Write failing test**

```go
// tokenize_test.go
package slackflag

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		err  bool
	}{
		{"empty", "", nil, false},
		{"single", "foo", []string{"foo"}, false},
		{"basic", "a b c", []string{"a", "b", "c"}, false},
		{"extra-spaces", "  -id  u123  -force  ", []string{"-id", "u123", "-force"}, false},
		{"double-quote", `-msg "hello world"`, []string{"-msg", "hello world"}, false},
		{"single-quote", `-msg 'hello world'`, []string{"-msg", "hello world"}, false},
		{"escaped-quote", `-msg "she said \"hi\""`, []string{"-msg", `she said "hi"`}, false},
		{"escaped-space", `a\ b`, []string{"a b"}, false},
		{"empty-string", `""`, []string{""}, false},
		{"unmatched-double", `"unterminated`, nil, true},
		{"unmatched-single", `'unterminated`, nil, true},
		{"trailing-backslash", `foo\`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenize(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("want err, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test, expect compile failure**

Run: `go test ./...`
Expected: build error — `undefined: tokenize`.

- [ ] **Step 3: Implement**

```go
// tokenize.go
package slackflag

import "fmt"

// tokenize splits a Slack slash command's text field into argv-style tokens.
// Supports double-quoted, single-quoted, and backslash-escaped sequences.
func tokenize(s string) ([]string, error) {
	var (
		tokens  []string
		cur     []byte
		inToken bool
	)
	push := func() {
		if inToken {
			tokens = append(tokens, string(cur))
			cur = cur[:0]
			inToken = false
		}
	}
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			push()
			i++
		case c == '"':
			inToken = true
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					cur = append(cur, s[i+1])
					i += 2
					continue
				}
				cur = append(cur, s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unmatched double quote")
			}
			i++
		case c == '\'':
			inToken = true
			i++
			for i < len(s) && s[i] != '\'' {
				cur = append(cur, s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unmatched single quote")
			}
			i++
		case c == '\\':
			if i+1 >= len(s) {
				return nil, fmt.Errorf("trailing backslash")
			}
			cur = append(cur, s[i+1])
			inToken = true
			i += 2
		default:
			cur = append(cur, c)
			inToken = true
			i++
		}
	}
	push()
	return tokens, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./...`
Expected: PASS, all 12 cases.

- [ ] **Step 5: Commit**

```bash
git add tokenize.go tokenize_test.go
git commit -m "feat: add shellwords-style tokenizer for slash text"
```

---

### Task 2: Signature verification

**Files:**
- Create: `signature.go`
- Create: `signature_test.go`

- [ ] **Step 1: Write failing test**

```go
// signature_test.go
package slackflag

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func computeSig(t *testing.T, secret, ts string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	secret := "test-secret"
	ts := "1714000000"
	body := []byte("token=abc&team_id=T1")
	sig := computeSig(t, secret, ts, body)
	now := time.Unix(1714000000, 0)

	if err := verifySignature(secret, ts, body, sig, now, 5*time.Minute); err != nil {
		t.Fatalf("good sig should pass: %v", err)
	}

	if err := verifySignature(secret, ts, body, "v0=deadbeef", now, 5*time.Minute); err == nil {
		t.Fatal("bad sig must fail")
	}

	tampered := []byte("token=abc&team_id=T2")
	if err := verifySignature(secret, ts, tampered, sig, now, 5*time.Minute); err == nil {
		t.Fatal("tampered body must fail")
	}

	later := now.Add(10 * time.Minute)
	if err := verifySignature(secret, ts, body, sig, later, 5*time.Minute); err == nil {
		t.Fatal("expired ts must fail")
	}

	if err := verifySignature(secret, "abc", body, sig, now, 5*time.Minute); err == nil {
		t.Fatal("non-numeric ts must fail")
	}

	if err := verifySignature("", ts, body, sig, now, 5*time.Minute); err == nil {
		t.Fatal("empty secret must fail")
	}
}
```

- [ ] **Step 2: Run, expect compile failure**

Run: `go test ./...`
Expected: `undefined: verifySignature`.

- [ ] **Step 3: Implement**

```go
// signature.go
package slackflag

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

func verifySignature(secret, ts string, body []byte, sig string, now time.Time, skew time.Duration) error {
	if secret == "" {
		return fmt.Errorf("empty signing secret")
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp %q: %w", ts, err)
	}
	delta := now.Sub(time.Unix(tsInt, 0))
	if delta > skew || delta < -skew {
		return fmt.Errorf("timestamp outside skew: %v", delta)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add signature.go signature_test.go
git commit -m "feat: add Slack request signature verification"
```

---

### Task 3: Block helpers

**Files:**
- Create: `block.go`
- Create: `block_test.go`

- [ ] **Step 1: Write failing test**

```go
// block_test.go
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
```

- [ ] **Step 2: Run, expect compile failure**

Run: `go test ./...`
Expected: undefined references.

- [ ] **Step 3: Implement**

```go
// block.go
package slackflag

// Block is anything that JSON-marshals to a valid Slack Block Kit block.
// Type alias keeps the package zero-dep but compatible with slack-go/slack types.
type Block = any

// Section returns a mrkdwn section block.
func Section(text string) Block {
	return map[string]any{
		"type": "section",
		"text": map[string]any{"type": "mrkdwn", "text": text},
	}
}

// Header returns a plain_text header block.
func Header(text string) Block {
	return map[string]any{
		"type": "header",
		"text": map[string]any{"type": "plain_text", "text": text, "emoji": true},
	}
}

// Fields returns a section with key/value mrkdwn fields. kv is flat: k1, v1, k2, v2, ...
// Panics if len(kv) is odd.
func Fields(kv ...string) Block {
	if len(kv)%2 != 0 {
		panic("slackflag.Fields: odd number of arguments")
	}
	fields := make([]map[string]any, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		fields = append(fields, map[string]any{
			"type": "mrkdwn",
			"text": "*" + kv[i] + "*\n" + kv[i+1],
		})
	}
	return map[string]any{
		"type":   "section",
		"fields": fields,
	}
}

// Divider returns a divider block.
func Divider() Block {
	return map[string]any{"type": "divider"}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add block.go block_test.go
git commit -m "feat: add Block type alias and helpers"
```

---

### Task 4: Response

**Files:**
- Create: `response.go`
- Create: `response_test.go`

- [ ] **Step 1: Write failing test**

```go
// response_test.go
package slackflag

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestResponseWrite(t *testing.T) {
	r := newResponse()
	fmt.Fprintf(r, "hello %s", "world")
	if string(r.text) != "hello world" {
		t.Fatalf("got %q", r.text)
	}
}

func TestResponseWriteBlocks(t *testing.T) {
	r := newResponse()
	r.WriteBlocks(Header("H"), Section("S"))
	if len(r.blocks) != 2 {
		t.Fatalf("got %d", len(r.blocks))
	}
}

func TestResponseFail(t *testing.T) {
	r := newResponse()
	r.Fail(errors.New("boom"))
	if !r.failed {
		t.Fatal("failed flag not set")
	}
	if r.failErr.Error() != "boom" {
		t.Fatal("err not stored")
	}
}

func TestResponseFlushBlocks(t *testing.T) {
	r := newResponse()
	fmt.Fprintf(r, "result: ok")
	r.WriteBlocks(Header("Done"))
	blocks := r.flushBlocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d", len(blocks))
	}
	b, _ := json.Marshal(blocks[0])
	if !strings.Contains(string(b), "result: ok") {
		t.Fatalf("text not first: %s", b)
	}
}

func TestResponseFlushOnFail(t *testing.T) {
	r := newResponse()
	fmt.Fprintf(r, "partial")
	r.Fail(errors.New("nope"))
	blocks := r.flushBlocks()
	if len(blocks) != 2 {
		t.Fatalf("expected partial section + err section, got %d", len(blocks))
	}
	b, _ := json.Marshal(blocks[1])
	if !strings.Contains(string(b), "nope") {
		t.Fatalf("err missing: %s", b)
	}
}

func TestResponseFlushEmpty(t *testing.T) {
	r := newResponse()
	if blocks := r.flushBlocks(); len(blocks) != 0 {
		t.Fatalf("empty response should flush to nothing, got %d blocks", len(blocks))
	}
}
```

- [ ] **Step 2: Run, expect compile failure**

Run: `go test ./...`

- [ ] **Step 3: Implement**

```go
// response.go
package slackflag

import "io"

// Response is the writer interface passed to Preview and Execute callbacks.
// It is NOT safe for concurrent use; if a handler spawns goroutines they
// must synchronize themselves.
type Response interface {
	io.Writer
	WriteBlocks(blocks ...Block)
	Fail(err error)
}

type response struct {
	text    []byte
	blocks  []Block
	failed  bool
	failErr error
}

func newResponse() *response { return &response{} }

func (r *response) Write(p []byte) (int, error) {
	r.text = append(r.text, p...)
	return len(p), nil
}

func (r *response) WriteBlocks(blocks ...Block) {
	r.blocks = append(r.blocks, blocks...)
}

func (r *response) Fail(err error) {
	r.failed = true
	r.failErr = err
}

// flushBlocks packages the accumulated content. Text becomes a leading
// mrkdwn section; structured blocks follow; on failure, an error section
// is appended.
func (r *response) flushBlocks() []Block {
	var out []Block
	if len(r.text) > 0 {
		out = append(out, Section(string(r.text)))
	}
	out = append(out, r.blocks...)
	if r.failed && r.failErr != nil {
		out = append(out, Section(":x: "+r.failErr.Error()))
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add response.go response_test.go
git commit -m "feat: add Response writer with text + blocks + Fail"
```

---

### Task 5: Command

**Files:**
- Create: `command.go`
- Create: `command_test.go`

- [ ] **Step 1: Write failing test**

```go
// command_test.go
package slackflag

import (
	"context"
	"flag"
	"testing"
)

func TestNewBasic(t *testing.T) {
	cmd := New("/foo", "do foo", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	})
	if cmd.Name != "/foo" || cmd.Description != "do foo" {
		t.Fatalf("got name=%q desc=%q", cmd.Name, cmd.Description)
	}
}

func TestNewPanicsOnEmptyName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	New("", "", func(fs *flag.FlagSet) Handlers { return Handlers{} })
}

func TestNewPanicsOnMissingSlash(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	New("foo", "", func(fs *flag.FlagSet) Handlers { return Handlers{} })
}

func TestNewPanicsOnNilBuild(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	New("/foo", "", nil)
}

func TestValidateHandlersPanicsOnNilExecute(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Preview: func(ctx context.Context, w Response) {}}
	})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	cmd.validateHandlers()
}

func TestValidateHandlersOKWithBoth(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{
			Preview: func(ctx context.Context, w Response) {},
			Execute: func(ctx context.Context, w Response) {},
		}
	})
	cmd.validateHandlers() // should not panic
}

func TestValidateHandlersOKWithExecuteOnly(t *testing.T) {
	cmd := New("/foo", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	})
	cmd.validateHandlers() // direct flow is allowed
}
```

- [ ] **Step 2: Run, expect compile failure**

Run: `go test ./...`

- [ ] **Step 3: Implement**

```go
// command.go
package slackflag

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

// Handlers carries the user-defined Preview and Execute callbacks.
// Preview is optional: a nil Preview switches the command to direct flow,
// where Execute runs immediately on slash invocation.
type Handlers struct {
	Preview func(ctx context.Context, w Response)
	Execute func(ctx context.Context, w Response)
}

// Command is a single slash command registration.
type Command struct {
	Name        string
	Description string
	build       func(*flag.FlagSet) Handlers
}

// New creates a new Command. name must start with "/". build is called once
// per request to construct a fresh FlagSet and Handlers (whose closures bind
// to that FlagSet's storage), keeping each request's state isolated.
func New(name, description string, build func(*flag.FlagSet) Handlers) *Command {
	if name == "" {
		panic("slackflag.New: empty name")
	}
	if !strings.HasPrefix(name, "/") {
		panic("slackflag.New: name must start with /")
	}
	if build == nil {
		panic("slackflag.New: nil build")
	}
	return &Command{Name: name, Description: description, build: build}
}

// validateHandlers builds the command once and panics if Execute is nil.
// Called by Mux.Register.
func (c *Command) validateHandlers() {
	fs := flag.NewFlagSet(c.Name, flag.ContinueOnError)
	h := c.build(fs)
	if h.Execute == nil {
		panic(fmt.Sprintf("slackflag: command %s has nil Execute", c.Name))
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add command.go command_test.go
git commit -m "feat: add Command type with New constructor and validation"
```

---

### Task 6: Metadata encode/replay

**Files:**
- Create: `metadata.go`
- Create: `metadata_test.go`

- [ ] **Step 1: Write failing test**

```go
// metadata_test.go
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
```

- [ ] **Step 2: Run, expect compile failure**

Run: `go test ./...`

- [ ] **Step 3: Implement**

```go
// metadata.go
package slackflag

import (
	"encoding/json"
	"flag"
	"fmt"
)

// metadata is what we store in Slack message metadata.event_payload.
type metadata struct {
	Args      map[string]string `json:"args"`
	Set       []string          `json:"set"`
	Invoker   string            `json:"invoker"`
	InvokedAt string            `json:"invoked_at"`
}

func encodeMetadata(fs *flag.FlagSet, invoker, invokedAt string) metadata {
	m := metadata{
		Args:      map[string]string{},
		Invoker:   invoker,
		InvokedAt: invokedAt,
	}
	fs.VisitAll(func(f *flag.Flag) {
		m.Args[f.Name] = f.Value.String()
	})
	fs.Visit(func(f *flag.Flag) {
		m.Set = append(m.Set, f.Name)
	})
	return m
}

func replayMetadata(fs *flag.FlagSet, m metadata) error {
	for _, name := range m.Set {
		v, ok := m.Args[name]
		if !ok {
			return fmt.Errorf("metadata: flag %q in Set but missing in Args", name)
		}
		if fs.Lookup(name) == nil {
			return fmt.Errorf("metadata: flag %q no longer defined (configuration drift)", name)
		}
		if err := fs.Set(name, v); err != nil {
			return fmt.Errorf("metadata: replay %q: %w", name, err)
		}
	}
	return nil
}

func (m metadata) marshal() (json.RawMessage, error) {
	return json.Marshal(m)
}

func unmarshalMetadata(data json.RawMessage) (metadata, error) {
	var m metadata
	if len(data) == 0 {
		return m, nil
	}
	err := json.Unmarshal(data, &m)
	return m, err
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add metadata.go metadata_test.go
git commit -m "feat: add FlagSet metadata encode/replay"
```

---

### Task 7: Slack API client

**Files:**
- Create: `slackapi.go`

This task adds the internal client. Tests come via the Mux integration tests that use `mockslack` (Task 8 onward). No standalone unit tests here — keeping this task small since it is plain HTTP plumbing.

- [ ] **Step 1: Implement**

```go
// slackapi.go
package slackflag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type slackClient struct {
	httpClient *http.Client
	baseURL    string // default https://slack.com/api
	botToken   string
}

// postResponseURL POSTs the given JSON-serializable body to a Slack response_url.
// Used for slash command followups and updating the original message after interactions.
func (c *slackClient) postResponseURL(ctx context.Context, url string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("response_url POST: %d: %s", resp.StatusCode, b)
	}
	return nil
}

// chatPostMessage posts a thread reply via Slack Web API chat.postMessage.
// Requires BotToken on the Mux Config.
func (c *slackClient) chatPostMessage(ctx context.Context, channel, threadTS string, blocks []Block) error {
	body := map[string]any{
		"channel":   channel,
		"thread_ts": threadTS,
		"blocks":    blocks,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat.postMessage", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.botToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chat.postMessage: %d: %s", resp.StatusCode, b)
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("chat.postMessage: %s", result.Error)
	}
	return nil
}
```

- [ ] **Step 2: Compile check**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add slackapi.go
git commit -m "feat: add internal Slack API client (response_url + chat.postMessage)"
```

---

### Task 8: Mock Slack server

**Files:**
- Create: `internal/mockslack/mockslack.go`

- [ ] **Step 1: Implement**

```go
// internal/mockslack/mockslack.go
package mockslack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Server captures incoming Slack API and response_url POSTs for assertion.
type Server struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	calls   []Call
	respURL string
}

type Call struct {
	URL    string         // either "/api/chat.postMessage" or "/response/<id>"
	Body   map[string]any // decoded JSON
	Header http.Header
}

// New starts an httptest.Server that mocks both Slack Web API endpoints
// (chat.postMessage) and a single response_url endpoint.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat.postMessage", s.handleAPI)
	mux.HandleFunc("/response/", s.handleResponseURL)
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	s.respURL = s.server.URL + "/response/test"
	return s
}

func (s *Server) URL() string         { return s.server.URL + "/api" }
func (s *Server) ResponseURL() string { return s.respURL }

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	s.mu.Lock()
	s.calls = append(s.calls, Call{URL: r.URL.Path, Body: decoded, Header: r.Header.Clone()})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleResponseURL(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	s.mu.Lock()
	s.calls = append(s.calls, Call{URL: r.URL.Path, Body: decoded, Header: r.Header.Clone()})
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// Calls returns a snapshot of received calls.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, len(s.calls))
	copy(out, s.calls)
	return out
}

// WaitFor polls until at least n calls have been received or timeout elapses.
// Useful because Mux dispatches handler logic on a goroutine.
func (s *Server) WaitFor(n int, timeout time.Duration) []Call {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c := s.Calls(); len(c) >= n {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for %d calls; got %d", n, len(s.Calls()))
	return nil
}

// SignedSlashRequest builds an *http.Request mimicking a Slack slash command
// signed with the given secret.
func SignedSlashRequest(t *testing.T, secret, command, text, userID, userName, channelID, responseURL string) *http.Request {
	t.Helper()
	form := url.Values{}
	form.Set("token", "irrelevant")
	form.Set("command", command)
	form.Set("text", text)
	form.Set("user_id", userID)
	form.Set("user_name", userName)
	form.Set("channel_id", channelID)
	form.Set("team_id", "T1")
	form.Set("response_url", responseURL)
	form.Set("trigger_id", "tr1")
	body := form.Encode()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/slack/command", nil)
	req.Body = io.NopCloser(stringReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

// SignedInteractionRequest builds an *http.Request mimicking a Slack
// block_actions interaction.
func SignedInteractionRequest(t *testing.T, secret string, payload map[string]any) *http.Request {
	t.Helper()
	pj, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	form.Set("payload", string(pj))
	body := form.Encode()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/slack/interact", nil)
	req.Body = io.NopCloser(stringReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

type stringReader string

func (s stringReader) Read(p []byte) (int, error) {
	if len(s) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s)
	return n, nil
}

// Reset clears recorded calls.
func (s *Server) Reset() {
	s.mu.Lock()
	s.calls = nil
	s.mu.Unlock()
}

func init() { _ = fmt.Sprint }
```

Note: `stringReader` is a one-shot reader for the test request body. The `init()` is to keep `fmt` imported even if used only in error messages.

- [ ] **Step 2: Compile check**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/mockslack/mockslack.go
git commit -m "test: add mock Slack server helper"
```

---

### Task 9: Mux core (NewMux, Register, dispatch)

**Files:**
- Create: `mux.go`
- Create: `mux_test.go`

This task lays the Mux skeleton: constructor, Register, command lookup, signature verification middleware. Slash and interaction handlers are stubbed and filled in subsequent tasks.

- [ ] **Step 1: Write failing test**

```go
// mux_test.go
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
```

- [ ] **Step 2: Run, expect failure**

Run: `go test ./...`

- [ ] **Step 3: Implement**

```go
// mux.go
package slackflag

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Config struct {
	SigningSecret string
	BotToken      string
	HTTPClient    *http.Client
	Logger        *slog.Logger
	SignatureSkew time.Duration
	SlackBaseURL  string
}

type Mux struct {
	cfg      Config
	logger   *slog.Logger
	client   *slackClient
	mu       sync.RWMutex
	commands map[string]*Command
	now      func() time.Time // for tests
}

func NewMux(cfg Config) *Mux {
	if cfg.SigningSecret == "" {
		panic("slackflag.NewMux: empty SigningSecret")
	}
	if cfg.BotToken == "" {
		panic("slackflag.NewMux: empty BotToken")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SignatureSkew == 0 {
		cfg.SignatureSkew = 5 * time.Minute
	}
	if cfg.SlackBaseURL == "" {
		cfg.SlackBaseURL = "https://slack.com/api"
	}
	return &Mux{
		cfg:      cfg,
		logger:   cfg.Logger,
		client:   &slackClient{httpClient: cfg.HTTPClient, baseURL: cfg.SlackBaseURL, botToken: cfg.BotToken},
		commands: map[string]*Command{},
		now:      time.Now,
	}
}

func (m *Mux) Register(cmd *Command) {
	cmd.validateHandlers()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.commands[cmd.Name]; ok {
		panic(fmt.Sprintf("slackflag: duplicate command %s", cmd.Name))
	}
	m.commands[cmd.Name] = cmd
}

func (m *Mux) lookup(name string) *Command {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.commands[name]
}

// readAndVerify reads the request body and verifies the Slack signature.
// On success returns the raw body bytes.
func (m *Mux) readAndVerify(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	ts := r.Header.Get("X-Slack-Request-Timestamp")
	sig := r.Header.Get("X-Slack-Signature")
	if err := verifySignature(m.cfg.SigningSecret, ts, body, sig, m.now(), m.cfg.SignatureSkew); err != nil {
		return nil, err
	}
	return body, nil
}

func (m *Mux) SlashHandler() http.Handler {
	return http.HandlerFunc(m.serveSlash)
}

func (m *Mux) InteractionHandler() http.Handler {
	return http.HandlerFunc(m.serveInteraction)
}

func (m *Mux) serveSlash(w http.ResponseWriter, r *http.Request) {
	_, err := m.readAndVerify(r)
	if err != nil {
		m.logger.Warn("slash signature invalid", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// TODO: filled in Task 10/11
	w.WriteHeader(http.StatusOK)
}

func (m *Mux) serveInteraction(w http.ResponseWriter, r *http.Request) {
	_, err := m.readAndVerify(r)
	if err != nil {
		m.logger.Warn("interaction signature invalid", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// TODO: filled in Task 12+
	w.WriteHeader(http.StatusOK)
}

// asyncRun runs fn in a goroutine with panic recovery, logging panics.
func (m *Mux) asyncRun(ctx context.Context, label string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error("panic in async runner", "label", label, "panic", r)
			}
		}()
		fn()
	}()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./...`
Expected: all 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add mux.go mux_test.go
git commit -m "feat: add Mux core (NewMux, Register, signature middleware)"
```

---

### Task 10: Slash handler — parse & ephemeral cases

**Files:**
- Modify: `mux.go` (replace `serveSlash`)
- Modify: `mux_test.go` (add tests)

This task wires the slash handler for: parse error → ephemeral usage; preview Fail → ephemeral; unknown command → ephemeral.

- [ ] **Step 1: Add tests**

Append to `mux_test.go`:

```go
import (
	"context"
	"flag"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ddzero2c/slackflag/internal/mockslack"
)

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
```

Add `import "fmt"` and `import "github.com/ddzero2c/slackflag/internal/mockslack"` to `mux_test.go` if missing.

- [ ] **Step 2: Replace `serveSlash` in `mux.go`**

```go
func (m *Mux) serveSlash(w http.ResponseWriter, r *http.Request) {
	body, err := m.readAndVerify(r)
	if err != nil {
		m.logger.Warn("slash signature invalid", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	cmdName := form.Get("command")
	cmd := m.lookup(cmdName)
	respURL := form.Get("response_url")
	userID := form.Get("user_id")
	userName := form.Get("user_name")
	channelID := form.Get("channel_id")
	text := form.Get("text")

	// Ack 200 immediately; do work asynchronously so we satisfy the 3s SLA.
	w.WriteHeader(http.StatusOK)

	m.asyncRun(r.Context(), "slash:"+cmdName, func() {
		if cmd == nil {
			m.postEphemeral(respURL, []Block{Section(":x: unknown command: " + cmdName)})
			return
		}
		fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
		var usageBuf bytes.Buffer
		fs.SetOutput(&usageBuf)
		h := cmd.build(fs)
		args, terr := tokenize(text)
		if terr != nil {
			m.postEphemeral(respURL, []Block{Section(":x: tokenize: " + terr.Error())})
			return
		}
		if err := fs.Parse(args); err != nil {
			fs.Usage()
			usage := usageBuf.String()
			if usage == "" {
				usage = err.Error()
			}
			m.postEphemeral(respURL, []Block{Section("```\n" + usage + "\n```")})
			return
		}

		ctx := context.Background()
		resp := newResponse()
		safeRun(m.logger, "preview "+cmd.Name, func() {
			if h.Preview != nil {
				h.Preview(ctx, resp)
			}
		}, resp)

		if resp.failed {
			m.postEphemeral(respURL, resp.flushBlocks())
			return
		}

		if h.Preview == nil {
			// Direct flow: Execute and post in_channel.
			execResp := newResponse()
			safeRun(m.logger, "execute "+cmd.Name, func() {
				h.Execute(ctx, execResp)
			}, execResp)
			marker := ":white_check_mark:"
			if execResp.failed {
				marker = ":x:"
			}
			footer := Section(fmt.Sprintf("%s ran by <@%s> at %s",
				marker, userName, m.now().Format("2006-01-02 15:04 MST")))
			blocks := append(execResp.flushBlocks(), footer)
			_ = m.client.postResponseURL(ctx, respURL, map[string]any{
				"response_type": "in_channel",
				"blocks":        blocks,
			})
			return
		}

		// Confirm flow: post preview with Confirm/Cancel buttons + metadata.
		md := encodeMetadata(fs, userID, m.now().UTC().Format(time.RFC3339))
		mdRaw, _ := md.marshal()
		blocks := append(resp.flushBlocks(),
			actionsBlock(cmd.Name),
		)
		_ = m.client.postResponseURL(ctx, respURL, map[string]any{
			"response_type": "in_channel",
			"blocks":        blocks,
			"metadata": map[string]any{
				"event_type":    "slackflag",
				"event_payload": json.RawMessage(mdRaw),
			},
		})
		_ = channelID // reserved for future use; not needed for response_url path
		_ = userName
	})
}

// postEphemeral pushes an ephemeral followup via response_url.
func (m *Mux) postEphemeral(respURL string, blocks []Block) {
	if respURL == "" {
		return
	}
	_ = m.client.postResponseURL(context.Background(), respURL, map[string]any{
		"response_type": "ephemeral",
		"blocks":        blocks,
	})
}

// safeRun wraps a handler invocation with panic recovery.
func safeRun(logger *slog.Logger, label string, fn func(), resp *response) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("handler panic", "label", label, "panic", r)
			resp.Fail(fmt.Errorf("panic: %v", r))
		}
	}()
	fn()
}

// actionsBlock returns the Confirm/Cancel buttons for a command.
func actionsBlock(cmdName string) Block {
	return map[string]any{
		"type": "actions",
		"elements": []map[string]any{
			{
				"type":      "button",
				"text":      map[string]any{"type": "plain_text", "text": "Confirm"},
				"style":     "primary",
				"action_id": "slackflag.confirm",
				"value":     cmdName,
			},
			{
				"type":      "button",
				"text":      map[string]any{"type": "plain_text", "text": "Cancel"},
				"style":     "danger",
				"action_id": "slackflag.cancel",
				"value":     cmdName,
			},
		},
	}
}
```

Add the new imports to `mux.go`:

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)
```

- [ ] **Step 3: Run tests**

Run: `go test ./...`
Expected: existing tests still pass; the four new slash tests pass.

- [ ] **Step 4: Commit**

```bash
git add mux.go mux_test.go
git commit -m "feat: slash handler — parse, ephemeral fallback, preview Fail, unknown command"
```

---

### Task 11: Slash handler — preview success & direct flow

**Files:**
- Modify: `mux_test.go` (add tests)

The implementation already supports both paths after Task 10. This task asserts the in_channel paths.

- [ ] **Step 1: Add tests**

```go
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
```

- [ ] **Step 2: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add mux_test.go
git commit -m "test: slash preview-success and direct-flow paths"
```

---

### Task 12: Interaction handler — confirm success

**Files:**
- Modify: `mux.go` (replace `serveInteraction`)
- Modify: `mux_test.go` (add tests)

- [ ] **Step 1: Add test**

```go
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
	calls := ms.WaitFor(2, 2*time.Second) // expect: thread post + response_url update

	var threadCall, updateCall *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "chat.postMessage") {
			threadCall = &calls[i]
		} else if strings.Contains(calls[i].URL, "/response/") {
			updateCall = &calls[i]
		}
	}
	if threadCall == nil || updateCall == nil {
		t.Fatalf("missing call: thread=%v update=%v calls=%v", threadCall, updateCall, calls)
	}
	if threadCall.Body["thread_ts"] != "1714000000.001" {
		t.Fatalf("thread_ts: %v", threadCall.Body["thread_ts"])
	}
	threadText := flattenBlocksText(threadCall.Body["blocks"].([]any))
	if !strings.Contains(threadText, "deleted u1") {
		t.Fatalf("thread reply: %s", threadText)
	}

	updateText := flattenBlocksText(updateCall.Body["blocks"].([]any))
	if strings.Contains(updateText, "Confirm") || strings.Contains(updateText, "Cancel") {
		t.Fatalf("buttons should be stripped: %s", updateText)
	}
	if !strings.Contains(updateText, "executed by <@alice>") {
		t.Fatalf("footer missing: %s", updateText)
	}
	if updateCall.Body["replace_original"] != true {
		t.Fatalf("replace_original missing")
	}
}
```

- [ ] **Step 2: Replace `serveInteraction` in `mux.go`**

```go
func (m *Mux) serveInteraction(w http.ResponseWriter, r *http.Request) {
	body, err := m.readAndVerify(r)
	if err != nil {
		m.logger.Warn("interaction signature invalid", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var payload struct {
		Type    string `json:"type"`
		User    struct{ ID, Name string } `json:"user"`
		Channel struct{ ID string }       `json:"channel"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
		Message struct {
			TS       string          `json:"ts"`
			Blocks   []any           `json:"blocks"`
			Metadata struct {
				EventType    string          `json:"event_type"`
				EventPayload json.RawMessage `json:"event_payload"`
			} `json:"metadata"`
		} `json:"message"`
		ResponseURL string `json:"response_url"`
	}
	if err := json.Unmarshal([]byte(form.Get("payload")), &payload); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	m.asyncRun(r.Context(), "interact", func() {
		if len(payload.Actions) == 0 {
			return
		}
		action := payload.Actions[0]
		md, _ := unmarshalMetadata(payload.Message.Metadata.EventPayload)

		// only original invoker may confirm or cancel
		if payload.User.ID != md.Invoker {
			m.postEphemeral(payload.ResponseURL, []Block{
				Section(fmt.Sprintf(":lock: only <@%s> can act on this command", md.Invoker)),
			})
			return
		}

		switch action.ActionID {
		case "slackflag.cancel":
			m.finalizePreview(payload.ResponseURL, payload.Message.Blocks,
				fmt.Sprintf(":no_entry_sign: cancelled by <@%s> at %s",
					payload.User.Name, m.now().Format("2006-01-02 15:04 MST")))
			return
		case "slackflag.confirm":
			cmd := m.lookup(action.Value)
			if cmd == nil {
				m.postEphemeral(payload.ResponseURL, []Block{Section(":x: unknown command: " + action.Value)})
				return
			}
			fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
			h := cmd.build(fs)
			if err := replayMetadata(fs, md); err != nil {
				m.postThread(payload.Channel.ID, payload.Message.TS,
					[]Block{Section(":x: " + err.Error())})
				return
			}
			ctx := context.Background()
			resp := newResponse()
			safeRun(m.logger, "execute "+cmd.Name, func() {
				h.Execute(ctx, resp)
			}, resp)
			threadBlocks := resp.flushBlocks()
			m.postThread(payload.Channel.ID, payload.Message.TS, threadBlocks)
			if resp.failed {
				// keep button alive — do NOT replace_original
				return
			}
			m.finalizePreview(payload.ResponseURL, payload.Message.Blocks,
				fmt.Sprintf(":white_check_mark: executed by <@%s> at %s",
					payload.User.Name, m.now().Format("2006-01-02 15:04 MST")))
		default:
			m.logger.Warn("unknown action_id", "id", action.ActionID)
		}
	})
}

// finalizePreview replaces the original preview message: strips actions blocks,
// appends a footer line.
func (m *Mux) finalizePreview(respURL string, original []any, footerText string) {
	stripped := stripActions(original)
	stripped = append(stripped, Section(footerText))
	_ = m.client.postResponseURL(context.Background(), respURL, map[string]any{
		"replace_original": true,
		"blocks":           stripped,
	})
}

// postThread fires a chat.postMessage as a thread reply.
func (m *Mux) postThread(channel, threadTS string, blocks []Block) {
	if err := m.client.chatPostMessage(context.Background(), channel, threadTS, blocks); err != nil {
		m.logger.Error("chat.postMessage failed", "err", err)
	}
}

// stripActions returns the blocks list without any "actions" blocks.
func stripActions(blocks []any) []Block {
	out := make([]Block, 0, len(blocks))
	for _, b := range blocks {
		if bm, ok := b.(map[string]any); ok && bm["type"] == "actions" {
			continue
		}
		out = append(out, b)
	}
	return out
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add mux.go mux_test.go
git commit -m "feat: interaction handler — confirm path with thread reply + footer"
```

---

### Task 13: Interaction handler — error paths (Fail, panic, non-invoker, cancel)

**Files:**
- Modify: `mux_test.go`

The implementation from Task 12 already covers these branches. This task adds tests asserting them.

- [ ] **Step 1: Add tests**

```go
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
	calls := ms.WaitFor(1, 2*time.Second)
	// must NOT include a response_url replace_original call
	for _, c := range calls {
		if strings.Contains(c.URL, "/response/") {
			if c.Body["replace_original"] == true {
				t.Fatalf("button should not be stripped on failure")
			}
		}
	}
	// thread reply must contain the err
	var thread *mockslack.Call
	for i := range calls {
		if strings.Contains(calls[i].URL, "chat.postMessage") {
			thread = &calls[i]
		}
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
	calls := ms.WaitFor(1, 2*time.Second)
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
```

- [ ] **Step 2: Run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add mux_test.go
git commit -m "test: interaction error paths (Fail, panic, non-invoker, cancel)"
```

---

### Task 14: Concurrency / race test

**Files:**
- Modify: `mux_test.go`

- [ ] **Step 1: Add test**

```go
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
```

- [ ] **Step 2: Run tests with race detector**

Run: `go test -race ./...`
Expected: PASS, no data races.

- [ ] **Step 3: Commit**

```bash
git add mux_test.go
git commit -m "test: concurrent slash invocations stay isolated"
```

---

### Task 15: Runnable example with smoke test

**Files:**
- Create: `examples/basic/main.go`
- Create: `examples/basic/main_test.go`

- [ ] **Step 1: Implement example**

```go
// examples/basic/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/ddzero2c/slackflag"
)

func deleteUserCmd() *slackflag.Command {
	return slackflag.New("/delete-user", "delete a user (demo)",
		func(fs *flag.FlagSet) slackflag.Handlers {
			id := fs.String("id", "", "user id")
			force := fs.Bool("force", false, "skip safety checks")
			return slackflag.Handlers{
				Preview: func(ctx context.Context, w slackflag.Response) {
					if *id == "" {
						w.Fail(fmt.Errorf("-id is required"))
						return
					}
					fmt.Fprintf(w, "about to delete user=%s force=%v", *id, *force)
				},
				Execute: func(ctx context.Context, w slackflag.Response) {
					fmt.Fprintf(w, "(demo) deleted user=%s", *id)
				},
			}
		})
}

func userStatsCmd() *slackflag.Command {
	return slackflag.New("/user-stats", "query user stats (demo, direct flow)",
		func(fs *flag.FlagSet) slackflag.Handlers {
			team := fs.String("team", "", "team id")
			return slackflag.Handlers{
				Execute: func(ctx context.Context, w slackflag.Response) {
					w.WriteBlocks(slackflag.Header("Stats: " + *team))
					fmt.Fprintf(w, "active=42 churned=3")
				},
			}
		})
}

func newServer(secret, token, slackBaseURL string) http.Handler {
	mux := slackflag.NewMux(slackflag.Config{
		SigningSecret: secret,
		BotToken:      token,
		SlackBaseURL:  slackBaseURL,
	})
	mux.Register(deleteUserCmd())
	mux.Register(userStatsCmd())
	root := http.NewServeMux()
	root.Handle("/slack/command", mux.SlashHandler())
	root.Handle("/slack/interact", mux.InteractionHandler())
	return root
}

func main() {
	secret := os.Getenv("SLACK_SIGNING_SECRET")
	token := os.Getenv("SLACK_BOT_TOKEN")
	if secret == "" || token == "" {
		log.Fatal("set SLACK_SIGNING_SECRET and SLACK_BOT_TOKEN")
	}
	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newServer(secret, token, "")))
}
```

- [ ] **Step 2: Implement smoke test**

```go
// examples/basic/main_test.go
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
```

- [ ] **Step 3: Run tests**

Run: `go test ./...`
Expected: PASS for everything in the module.

Run: `go test -race ./...`
Expected: PASS, no races.

- [ ] **Step 4: Verify example builds**

Run: `go build ./examples/basic`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add examples/basic/main.go examples/basic/main_test.go
git commit -m "feat: add basic example with smoke tests"
```

---

## Self-review notes (already applied)

- Spec coverage:
  - ✅ Tokenize → Task 1
  - ✅ Signature verification → Task 2
  - ✅ Block helpers → Task 3
  - ✅ Response interface (Write, WriteBlocks, Fail) → Task 4
  - ✅ Command + Handlers + New + register-time validation → Task 5, validated at Mux.Register in Task 9
  - ✅ Metadata encode/replay (set vs default, drift) → Task 6
  - ✅ Slack API client (response_url, chat.postMessage) → Task 7
  - ✅ Mock Slack server → Task 8
  - ✅ Mux core (NewMux, Register, signature middleware) → Task 9
  - ✅ Slash flow (parse error, ephemeral, preview success, direct flow, direct flow Fail) → Tasks 10-11
  - ✅ Interaction flow (confirm, cancel, Fail-preserves-button, panic, non-invoker) → Tasks 12-13
  - ✅ Concurrency / race → Task 14
  - ✅ Example with smoke test → Task 15
- Type consistency: `Response` interface methods (`Write`, `WriteBlocks`, `Fail`) are stable across all tasks. `Block` is `any` throughout. `metadata` shape (`Args`, `Set`, `Invoker`, `InvokedAt`) is stable.
- Placeholder scan: no TBDs or vague steps; every step has runnable code/commands.
