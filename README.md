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

## Proactive messages and interaction routing

Slash commands aren't the only way to put buttons in a channel. `Poster` sends
a proactive `chat.postMessage` to any channel, and `Mux.HandleAction` routes
clicks on its buttons to a handler keyed by exact `action_id`. The slash command
confirm/cancel flow is independent and unaffected.

```go
poster := slackflag.NewPoster(cfg) // same Config as NewMux

// Post an approval card. The button value carries your context (an order id),
// and Metadata round-trips back to the handler as Interaction.Metadata.
ts, err := poster.Post(ctx, slackflag.Message{
    Channel:  "C0123ABCDEF",
    Fallback: "Order ord-1001 needs approval", // notification + a11y text
    Blocks: []slackflag.Block{
        slackflag.Header("Order ord-1001"),
        slackflag.Section("A new order needs your approval."),
        slackflag.Actions(
            slackflag.Button("order.approve", "Approve", "ord-1001", "primary"),
            slackflag.Button("order.reject", "Reject", "ord-1001", "danger"),
        ),
    },
    Metadata: map[string]any{"order_id": "ord-1001"},
})

// Route clicks. Blocks written to w replace the original message; returning an
// error (or w.Fail) restores the buttons with an error note so the user can
// retry. action_ids starting with "slackflag." are reserved and panic.
mux.HandleAction("order.approve", func(ctx context.Context, ic slackflag.Interaction, w slackflag.Response) error {
    fmt.Fprintf(w, ":white_check_mark: order %s approved by <@%s>", ic.Value, ic.UserName)
    return nil
})
```

`Interaction` carries `ActionID`, `Value`, `UserID`, `UserName`, `Channel`,
`MessageTS` (for a later `chat.update`), and `Metadata`. On a click slackflag
strips the buttons immediately (closing the double-click window) before running
the handler, mirroring the confirm/cancel lifecycle.

`Poster` is stateless — no scheduling, retry, or outbox. Pairing a send with a
database transaction, and handing long work to your own queue (Slack expects an
ack within 3 seconds), are the caller's responsibility.

## Relationship to slack-go

slackflag is a thin framework layer **on top of** `github.com/slack-go/slack`,
not a replacement — it adds the slash-command, Preview/Confirm lifecycle, and
interaction-routing plumbing that slack-go doesn't, and delegates block types,
marshaling, and the Web API client to slack-go. The two interoperate, and there
are escape hatches wherever slackflag's convenience API isn't enough:

- `slackflag.Block` / `slackflag.Element` are aliases for `slack.Block` /
  `slack.BlockElement`, so slackflag and slack-go blocks are the same values —
  pass either where the other is expected.
- `Interaction.Raw` is the full `*slack.InteractionCallback` — reach for
  `trigger_id`, `team`, `view`, `state`, or anything else slackflag doesn't surface.
- `Message.Options []slack.MsgOption` are appended verbatim to the
  `chat.postMessage` call (e.g. `slack.MsgOptionTS(...)` to thread a reply).
- `Poster.Client` is the underlying `*slack.Client` for API calls slackflag
  doesn't wrap (`chat.update`, scheduling, reactions, ...).

See `examples/basic` for a runnable example.
