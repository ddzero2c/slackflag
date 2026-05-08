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

// Command is a slash command or subcommand registration. A Command may be a
// leaf (with handlers via build) or a branch (with subcommands added via
// AddSubcommand). Branch commands have build == nil; the slash invocation
// renders the subcommand list as help.
type Command struct {
	Name        string
	Description string
	build       func(*flag.FlagSet) Handlers
	parent      *Command
	subs        map[string]*Command
	subOrder    []string
}

// New creates a Command. For top-level commands registered via Mux.Register,
// name must start with "/" (Mux.Register enforces this). For subcommands
// added via AddSubcommand, name must NOT start with "/" (AddSubcommand
// enforces this). build may be nil for branch commands that only host
// subcommands; if non-nil, it is called once per request to construct a
// fresh FlagSet and Handlers (whose closures bind to that FlagSet's
// storage), keeping each request's state isolated.
func New(name, description string, build func(*flag.FlagSet) Handlers) *Command {
	if name == "" {
		panic("slackflag.New: empty name")
	}
	return &Command{Name: name, Description: description, build: build}
}

// AddSubcommand attaches a subcommand. The subcommand's Name must not start
// with "/" and must be unique among siblings. Returns the parent Command for
// chaining. Panics if sub is already attached to a parent.
func (c *Command) AddSubcommand(sub *Command) *Command {
	if sub == nil {
		panic("slackflag.AddSubcommand: nil sub")
	}
	if sub.Name == "" {
		panic("slackflag.AddSubcommand: empty name")
	}
	if strings.HasPrefix(sub.Name, "/") {
		panic(fmt.Sprintf("slackflag.AddSubcommand: subcommand name %q must not start with /", sub.Name))
	}
	if sub.parent != nil {
		panic(fmt.Sprintf("slackflag.AddSubcommand: %q already attached to a parent", sub.Name))
	}
	if c.subs == nil {
		c.subs = map[string]*Command{}
	}
	if _, ok := c.subs[sub.Name]; ok {
		panic(fmt.Sprintf("slackflag.AddSubcommand: duplicate subcommand %q", sub.Name))
	}
	sub.parent = c
	c.subs[sub.Name] = sub
	c.subOrder = append(c.subOrder, sub.Name)
	return c
}

// validateHandlers walks the command tree. Each leaf must have a non-nil
// build that produces a non-nil Execute. Branch commands (those with
// subcommands) must have build == nil; they render help when invoked.
// Called by Mux.Register.
func (c *Command) validateHandlers() {
	if len(c.subs) > 0 {
		if c.build != nil {
			panic(fmt.Sprintf("slackflag: command %s has subcommands; build must be nil (branch commands cannot have their own handlers in this version)", c.Name))
		}
		for _, name := range c.subOrder {
			c.subs[name].validateHandlers()
		}
		return
	}
	if c.build == nil {
		panic(fmt.Sprintf("slackflag: command %s has no Execute and no subcommands", c.Name))
	}
	fs := flag.NewFlagSet(c.Name, flag.ContinueOnError)
	h := c.build(fs)
	if h.Execute == nil {
		panic(fmt.Sprintf("slackflag: command %s has nil Execute", c.Name))
	}
}
