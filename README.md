# slackflag

A small framework for building Slack admin slash commands with `flag`-style
arguments, optional Preview/Confirm flow, and signature verification.

## Subcommands

A single Slack slash registration can host many operations. Use
`AddSubcommand` to attach subcommands to a top-level command:

```go
admin := slackflag.New("/admin", "internal admin tools", nil) // branch: nil build
admin.AddSubcommand(slackflag.New("user-create", "create a user",
    func(fs *flag.FlagSet) slackflag.Handlers {
        name := fs.String("name", "", "user name")
        role := fs.String("role", "viewer", "user role")
        return slackflag.Handlers{
            Validate: func() error {
                if *name == "" {
                    return fmt.Errorf("-name is required")
                }
                return nil
            },
            Preview: func(ctx context.Context, w slackflag.Response) {
                fmt.Fprintf(w, "creating user=%s role=%s", *name, *role)
            },
            Execute: func(ctx context.Context, w slackflag.Response) {
                createUser(*name, *role)
            },
        }
    }))
admin.AddSubcommand(slackflag.New("feature-flag", "toggle a flag", featureFlagHandlers))

mux.Register(admin)
```

Behaviour:

- `/admin` or `/admin help` or `/admin -h` → ephemeral list of subcommands.
- `/admin user-create -name=alice -role=editor` → runs `user-create` (with
  Preview/Confirm or direct flow as configured per subcommand).
- `/admin user-create -h` → flag usage for that subcommand.
- `/admin nonexistent` → ephemeral error + list of subcommands.

The Confirm-button metadata records the subcommand path
(`{"sub":["user-create"], …}`), so a clicked Confirm replays into the right
subcommand even after the message has been sitting in the channel.

Limitation: top-level flags before the subcommand
(`/admin -dry-run user-create …`) are not supported in this version. The
first positional token must be the subcommand name.

See `examples/basic` for a runnable example.
