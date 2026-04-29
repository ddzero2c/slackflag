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
