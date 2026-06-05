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
				Validate: func() error {
					if *id == "" {
						return fmt.Errorf("-id is required")
					}
					return nil
				},
				Preview: func(ctx context.Context, w slackflag.Response) {
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

// adminCmd shows the subcommand pattern: a single /admin slash registration
// hosts multiple operations as subcommands. /admin alone (or /admin -h)
// renders an auto-generated list of available subcommands.
func adminCmd() *slackflag.Command {
	admin := slackflag.New("/admin", "internal admin tools", nil)

	admin.AddSubcommand(slackflag.New("user-create", "create a user (confirm flow)",
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
					fmt.Fprintf(w, "about to create user=%s role=%s", *name, *role)
				},
				Execute: func(ctx context.Context, w slackflag.Response) {
					fmt.Fprintf(w, "(demo) created user=%s role=%s", *name, *role)
				},
			}
		}))

	admin.AddSubcommand(slackflag.New("feature-flag", "toggle a feature flag (direct flow)",
		func(fs *flag.FlagSet) slackflag.Handlers {
			name := fs.String("name", "", "flag name")
			on := fs.Bool("on", false, "enable the flag")
			return slackflag.Handlers{
				Execute: func(ctx context.Context, w slackflag.Response) {
					state := "off"
					if *on {
						state = "on"
					}
					fmt.Fprintf(w, "(demo) feature %s set %s", *name, state)
				},
			}
		}))

	return admin
}

// registerOrderActions wires handlers for button clicks on proactive messages.
// These buttons are placed by postOrderCard (via Poster), not by a slash
// command, so they route through HandleAction by exact action_id. The original
// slash command confirm/cancel flow is unaffected.
func registerOrderActions(mux *slackflag.Mux) {
	mux.HandleAction("order.approve", func(ctx context.Context, ic slackflag.Interaction, w slackflag.Response) error {
		fmt.Fprintf(w, ":white_check_mark: order %s approved by <@%s>", ic.Value, ic.UserName)
		return nil
	}).HandleAction("order.reject", func(ctx context.Context, ic slackflag.Interaction, w slackflag.Response) error {
		fmt.Fprintf(w, ":x: order %s rejected by <@%s>", ic.Value, ic.UserName)
		return nil
	})
}

// postOrderCard sends a proactive approval card to a channel and returns the
// posted message ts. Clicks on its buttons route to registerOrderActions.
func postOrderCard(ctx context.Context, p *slackflag.Poster, channel, orderID string) (string, error) {
	return p.Post(ctx, slackflag.Message{
		Channel:  channel,
		Fallback: "Order " + orderID + " needs approval",
		Blocks: []slackflag.Block{
			slackflag.Header("Order " + orderID),
			slackflag.Section("A new order needs your approval."),
			slackflag.Actions(
				slackflag.Button("order.approve", "Approve", orderID, "primary"),
				slackflag.Button("order.reject", "Reject", orderID, "danger"),
			),
		},
		Metadata: map[string]any{"order_id": orderID},
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
	mux.Register(adminCmd())
	registerOrderActions(mux)
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

	// Optionally fire a proactive approval card on startup so the outbound +
	// routing path can be exercised end-to-end: set DEMO_CHANNEL=C0123ABCDEF.
	if ch := os.Getenv("DEMO_CHANNEL"); ch != "" {
		poster := slackflag.NewPoster(slackflag.Config{BotToken: token})
		ts, err := postOrderCard(context.Background(), poster, ch, "ord-1001")
		if err != nil {
			log.Printf("demo post failed: %v", err)
		} else {
			log.Printf("posted approval card to %s (ts=%s)", ch, ts)
		}
	}

	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newServer(secret, token, "")))
}
