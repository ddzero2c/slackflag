package slackflag

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

// Handlers carries the user-defined callbacks for a command.
// Validate and Preview are optional. A nil Preview switches to direct flow
// (Execute runs immediately). Validate runs after flag parsing; a non-nil
// error is sent as an ephemeral and stops further execution.
type Handlers struct {
	Validate func() error
	Preview  func(ctx context.Context, w Response)
	Execute  func(ctx context.Context, w Response)
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
