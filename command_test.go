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
