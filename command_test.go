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

func TestValidateHandlersPanicsOnNilBuildNoSubs(t *testing.T) {
	cmd := New("/foo", "", nil)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for leaf with nil build")
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

func TestValidateHandlersBranchOK(t *testing.T) {
	parent := New("/admin", "admin tools", nil)
	parent.AddSubcommand(New("user-create", "create user", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	}))
	parent.validateHandlers() // branch with nil build + valid sub is fine
}

func TestValidateHandlersBranchWithBuildPanics(t *testing.T) {
	parent := New("/admin", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	})
	parent.AddSubcommand(New("sub", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	}))
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic: branch with build")
		}
	}()
	parent.validateHandlers()
}

func TestValidateHandlersRecursesIntoSubs(t *testing.T) {
	parent := New("/admin", "", nil)
	parent.AddSubcommand(New("broken", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Preview: func(ctx context.Context, w Response) {}}
	}))
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic from sub with nil Execute")
		}
	}()
	parent.validateHandlers()
}

func TestAddSubcommandPanicsOnLeadingSlash(t *testing.T) {
	parent := New("/admin", "", nil)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on leading slash")
		}
	}()
	parent.AddSubcommand(New("/sub", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	}))
}

func TestAddSubcommandPanicsOnDuplicate(t *testing.T) {
	parent := New("/admin", "", nil)
	parent.AddSubcommand(New("sub", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	}))
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate sub name")
		}
	}()
	parent.AddSubcommand(New("sub", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	}))
}

func TestAddSubcommandPanicsOnDoubleParent(t *testing.T) {
	sub := New("sub", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	})
	a := New("/a", "", nil)
	a.AddSubcommand(sub)
	b := New("/b", "", nil)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on re-parenting")
		}
	}()
	b.AddSubcommand(sub)
}

func TestAddSubcommandPanicsOnEmptyName(t *testing.T) {
	parent := New("/admin", "", nil)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty sub name")
		}
	}()
	// Must construct via New to bypass empty-name check, so build directly.
	parent.AddSubcommand(&Command{Name: ""})
}

func TestAddSubcommandReturnsParentForChaining(t *testing.T) {
	parent := New("/admin", "", nil)
	got := parent.AddSubcommand(New("sub", "", func(fs *flag.FlagSet) Handlers {
		return Handlers{Execute: func(ctx context.Context, w Response) {}}
	}))
	if got != parent {
		t.Fatalf("AddSubcommand should return parent for chaining")
	}
}
