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
				Preview: func(ctx context.Context, w slackflag.Response) {
					if *id == "" {
						w.Fail(fmt.Errorf("-id is required"))
						return
					}
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

func newServer(secret, token, slackBaseURL string) http.Handler {
	mux := slackflag.NewMux(slackflag.Config{
		SigningSecret: secret,
		BotToken:      token,
		SlackBaseURL:  slackBaseURL,
	})
	mux.Register(deleteUserCmd())
	mux.Register(userStatsCmd())
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
	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newServer(secret, token, "")))
}
