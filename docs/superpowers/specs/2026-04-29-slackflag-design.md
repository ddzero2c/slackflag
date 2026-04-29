# slackflag — Design Spec

**Date:** 2026-04-29
**Module:** `github.com/ddzero2c/slackflag`
**Go:** 1.26.1

## Goal

Provide a Go package that abstracts a recurring pattern: Slack slash commands used as a backend admin interface, where each command:

1. Parses arguments via `flag.FlagSet` (stdlib semantics, automatic help on parse error or `-h`).
2. Shows a **preview** message in-channel with a **Confirm** button.
3. On confirm, runs an **execute** step. The result is posted as a **thread reply** under the preview message, and the original preview's button is replaced with a footer recording who executed and when.
4. On execute failure, the error is posted as a thread reply but the Confirm button is preserved so the action can be retried.

The package targets internal team admin tools, not public-facing bots.

## Non-goals

- Modal-based form input (`views.open`); future work.
- Stateful queue / approval workflows beyond a single confirm step.
- Slack Socket Mode; HTTP webhook only.
- A `slack-go/slack` dependency; types stay zero-dep but compatible.
- Built-in two-person approval / RBAC; per-command policy is a future opt-in.

## Public API

### Top-level

```go
package slackflag

type Mux struct{ /* ... */ }
type Command struct{ /* ... */ }
type Handlers struct {
    Preview func(ctx context.Context, w Response) // optional; nil → direct flow
    Execute func(ctx context.Context, w Response) // required
}

type Response interface {
    io.Writer                          // text → mrkdwn section on flush
    WriteBlocks(blocks ...Block)       // append structured blocks
    Fail(err error)                    // mark failure path
}

type Block = any // type alias; anything that JSON-marshals to a Slack block

type Config struct {
    SigningSecret string         // required (NewMux panics if empty)
    BotToken      string         // required (NewMux panics if empty); used for chat.postMessage thread reply
    HTTPClient    *http.Client   // optional
    Logger        *slog.Logger   // optional, default slog.Default()
    SignatureSkew time.Duration  // optional, default 5 * time.Minute
    SlackBaseURL  string         // optional; default https://slack.com/api (test hook)
}

func NewMux(cfg Config) *Mux
func (m *Mux) Register(cmd *Command)
func (m *Mux) SlashHandler() http.Handler
func (m *Mux) InteractionHandler() http.Handler

func New(name, description string, build func(*flag.FlagSet) Handlers) *Command

// Block helpers
func Section(text string) Block          // mrkdwn section
func Header(text string) Block           // header
func Fields(kv ...string) Block          // section with fields []
func Divider() Block
```

### Usage example (confirm flow)

```go
cmd := slackflag.New("/delete-user", "delete a user",
    func(fs *flag.FlagSet) slackflag.Handlers {
        id := fs.String("id", "", "user id")
        force := fs.Bool("force", false, "skip safety checks")
        return slackflag.Handlers{
            Preview: func(ctx context.Context, w slackflag.Response) {
                if !userExists(*id) {
                    w.Fail(fmt.Errorf("user %s not found", *id))
                    return
                }
                fmt.Fprintf(w, "about to delete user=%s force=%v", *id, *force)
            },
            Execute: func(ctx context.Context, w slackflag.Response) {
                if err := doDelete(ctx, *id, *force); err != nil {
                    w.Fail(err)
                    return
                }
                fmt.Fprintf(w, "deleted user=%s", *id)
            },
        }
    })

mux := slackflag.NewMux(slackflag.Config{
    SigningSecret: os.Getenv("SLACK_SIGNING_SECRET"),
    BotToken:      os.Getenv("SLACK_BOT_TOKEN"),
})
mux.Register(cmd)
http.Handle("/slack/command",  mux.SlashHandler())
http.Handle("/slack/interact", mux.InteractionHandler())
```

### Usage example (direct flow, read-only)

```go
cmd := slackflag.New("/user-stats", "query user stats",
    func(fs *flag.FlagSet) slackflag.Handlers {
        team := fs.String("team", "", "team id")
        return slackflag.Handlers{
            // Preview omitted → direct flow
            Execute: func(ctx context.Context, w slackflag.Response) {
                stats, err := queryStats(ctx, *team)
                if err != nil { w.Fail(err); return }
                w.WriteBlocks(slackflag.Header("Stats: " + *team))
                fmt.Fprintf(w, "active=%d, churned=%d", stats.Active, stats.Churned)
            },
        }
    })
```

## Behavior matrix

### Preview presence

| `Handlers.Preview` | Slash → behavior |
|---|---|
| non-nil | preview message in_channel + Confirm/Cancel buttons; on Confirm → Execute → thread reply + button replaced with footer |
| nil | Execute runs immediately; result posted in_channel with footer "ran by @user at \<time\>" |

### Response semantics

| Phase | No `Fail` called | `Fail(err)` called |
|---|---|---|
| Preview | content → in_channel preview msg + Confirm/Cancel buttons | content (incl. err) → ephemeral to invoker; nothing published |
| Execute (confirm flow) | content → thread reply on preview; preview button → footer "executed by @user at \<time\>" | content (incl. err) → thread reply; preview button preserved |
| Execute (direct flow) | content → in_channel msg with footer "ran by @user at \<time\>" | content → in_channel msg with ❌ marker + footer |

### Cancel button

Pressed only by the original invoker (always restricted, regardless of policy). On cancel:
- preview button replaced with `🚫 cancelled by @user at <time>`
- no thread reply

### Confirm restriction

Default: only the original invoker (`user_id` from slash command, stored in metadata) can confirm. Other users get an ephemeral "only @\<invoker\> can confirm". A future opt-in `Command.WithApprover(...)` can flip this to a two-person rule.

## Architecture

```
Slack ──slash POST───► mux.SlashHandler ─► verify sig ─► dispatch by /command
                                                          │
                                                          ▼
                                                       Command.Build(fs)
                                                          │
                                                          ▼
                                                       fs.Parse(text)
                                                       ├─ err / -h ─► ephemeral usage
                                                       └─ ok ──────► Preview() → in_channel msg
                                                                     (blocks + Confirm/Cancel,
                                                                      metadata: parsed args + invoker)

Slack ──interact POST─► mux.InteractionHandler ─► verify sig ─► parse payload
                                                                    │
                                                                    ▼
                                                                action_id => command name
                                                                metadata  => parsed args
                                                                    │
                                                                    ▼
                                                                Command.Build(fs)
                                                                fs.Set(...) replay
                                                                    │
                                                                    ▼
                                                                 Execute(ctx, w)
                                                                    │
                                              ┌─────────────────────┴────────────────────┐
                                              ▼                                          ▼
                                         w.Fail called                              normal return
                                              │                                          │
                                  thread reply: ❌ + content                   update preview: btn → footer
                                  preview unchanged (btn live)                 thread reply: w content
```

### Slack 3-second SLA

All HTTP handlers ack `200` immediately, then run handler logic in a goroutine and POST results via `response_url` (preview) or `chat.postMessage` (thread reply). This means slash and interaction handlers are non-blocking from Slack's perspective.

### Args persistence between Preview and Execute

Slack message **`metadata.event_payload`** carries the parsed args between the two requests:

```json
{
  "args":       { "id": "u123", "force": "true" },
  "set":        ["id", "force"],
  "invoker":    "U001",
  "invoked_at": "2026-04-29T15:50:00Z"
}
```

- `args` map: every flag name → `flag.Value.String()` (works with `flag.Var` custom types).
- `set` list: only flags actually set by the user; on replay, unset flags keep their default.
- On Confirm: build a fresh `*flag.FlagSet` via `cmd.Build`, replay `fs.Set(name, args[name])` for each name in `set`, then run `Execute`. Closures over typed pointers (`*string`, `*bool`, custom `flag.Var`) work transparently.

This sidesteps server-side state and keeps the package stateless. It is also thread-safe: every request gets a fresh FlagSet, so concurrent invocations of the same command never share storage.

## Components

| File | Responsibility | Exported |
|---|---|---|
| `mux.go` | HTTP routing, signature verification, command lookup, payload dispatch, async fire-and-forget for Slack 3s SLA | `Mux`, `Config`, `NewMux` |
| `command.go` | `Command` type, `Handlers` struct, `New`, register-time validation (Execute required) | `Command`, `Handlers`, `New` |
| `response.go` | `Response` interface and internal impl (text buffer, blocks accumulator, `failed` flag, `failErr`) | `Response` |
| `block.go` | `Block` type alias, helpers (`Section`, `Header`, `Fields`, `Divider`) | `Block`, helpers |
| `metadata.go` | encode/replay between FlagSet and Slack message metadata | (internal) |
| `slackapi.go` | thin client over `chat.postMessage`, `response_url` POST, `chat.update` | (internal) |
| `signature.go` | HMAC-SHA256 verification with timestamp-skew check | (internal) |
| `tokenize.go` | shellwords-style tokenizer for slash `text` (quotes, escape) | (internal) |

## Error handling

| Error point | Treatment | Visibility |
|---|---|---|
| Slack signature mismatch | 401 + log | server-side |
| Timestamp outside skew | 401 + log (replay defence) | server-side |
| Body decode failure | 400 + log | server-side |
| Unknown command | 200 ephemeral "unknown command: /xxx" | invoker only |
| `fs.Parse` error or `-h` | 200 ephemeral with `fs.PrintDefaults()` output | invoker only |
| Preview `w.Fail(err)` | ephemeral with err message + any prior buffer content | invoker only |
| Preview panic | recover → treat as `Fail(fmt.Errorf("panic: %v", r))`; stack to server log | invoker only |
| Confirm by non-invoker (when restricted) | ephemeral "only @\<invoker\> can confirm" | clicker only |
| Execute `w.Fail(err)` (confirm flow) | thread reply with content + err; preview button preserved | in_channel |
| Execute `w.Fail(err)` (direct flow) | in_channel msg with ❌ marker + content + footer | in_channel |
| Execute panic | recover → `Fail(fmt.Errorf("panic: %v", r))`; stack to server log | same as `Fail` for the active flow |
| `chat.postMessage` failure (thread) | log + fallback: replace_original via `response_url` with result inlined | in_channel |
| Replay flag drift (flag no longer defined) | thread reply "configuration drift: flag '-x' no longer defined"; button preserved | in_channel |

### Recovery rules

- **Panic recovery is always on.** Every user callback (`Preview`, `Execute`) is wrapped with `defer recover()`. A buggy command must not crash the Mux.
- **Logger** receives every server-side error via `cfg.Logger` (default `slog.Default()`), with structured fields: `command`, `user_id`, `team_id`, `channel_id`.
- **Response is not concurrent-safe.** It is single-handler-only. Handlers spawning goroutines must synchronize themselves; this is documented on the interface.

## Testing strategy

| Layer | Approach | Coverage |
|---|---|---|
| `signature` | unit, table-driven | correct sig passes; bad sig fails; expired ts fails; edge cases (empty body, malformed header) |
| `metadata` Encode/Replay | unit | round-trip equality including `flag.Var` custom types and set-vs-default distinction |
| `tokenize` | unit, table-driven | double quote, single quote, backslash escape, unmatched quote → error |
| `Response` | unit | Write + WriteBlocks accumulation; Fail flag; buffer flush ordering |
| `Command` validate | unit | nil Execute panics at Register; nil Preview is allowed (direct flow) |
| `Mux` slash flow | integration with mock Slack via `httptest.Server` | parse error → ephemeral; preview success → in_channel with metadata; preview Fail → ephemeral; direct flow → in_channel with footer |
| `Mux` interaction flow | integration | confirm by invoker → thread + footer; confirm by other → ephemeral; cancel → footer; execute Fail → button preserved + thread err; panic → recovered |
| Concurrency | `go test -race` with concurrent slash invocations | two users running same command simultaneously do not pollute each other's FlagSet |

### Mock Slack server

`internal/mockslack` test helper: an `httptest.Server` that captures incoming `chat.postMessage` / `response_url` POSTs and exposes `ms.Messages()` for assertions. Wired in tests via `Config.SlackBaseURL`.

```go
ms := mockslack.New(t)
mux := slackflag.NewMux(slackflag.Config{
    SigningSecret: "test",
    BotToken:      "xoxb-test",
    SlackBaseURL:  ms.URL,
})
mux.Register(cmd)

req := mockslack.SignedSlashRequest(t, "test", "/delete-user -id u1", ...)
mux.SlashHandler().ServeHTTP(w, req)

assert.Equal(t, ms.LastResponse().Type, "in_channel")
```

### E2E

Not in CI (would require live Slack workspace and token). README provides a manual smoke-test checklist for new contributors.

## Open questions / future work

- **Approver policy:** `Command.WithApprover(func(invoker, clicker User) bool)` for two-person rule. Out of scope for v1.
- **Modal input:** `views.open` for sensitive args (passwords, large text). Out of scope for v1.
- **Streaming output:** if Execute wants to flush partial progress to thread, a future `Response.Flush()` could push intermediate thread replies. v1 buffers everything until handler returns.
- **Repeatable commands:** out of scope. Confirm-flow buttons lock on success; idempotent re-runs require re-running the slash command.
