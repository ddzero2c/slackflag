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
