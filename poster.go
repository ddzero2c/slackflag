package slackflag

import (
	"context"
	"strings"

	"github.com/slack-go/slack"
)

// Poster sends proactive (outbound) messages to arbitrary channels via
// chat.postMessage. Unlike the slash/interaction flow it is not tied to a
// response_url: callers use it to start a conversation, then route any button
// clicks on that message through Mux.HandleAction.
//
// Poster is stateless. It does no scheduling, retrying, or outbox bookkeeping;
// pairing a send with a database transaction is the caller's responsibility.
type Poster struct {
	api *slack.Client
}

// NewPoster builds a Poster from the same Config used for NewMux. Only BotToken,
// HTTPClient, and SlackBaseURL are consulted. It does not validate the token, so
// it never panics.
func NewPoster(cfg Config) *Poster {
	var opts []slack.Option
	if cfg.HTTPClient != nil {
		opts = append(opts, slack.OptionHTTPClient(cfg.HTTPClient))
	}
	if cfg.SlackBaseURL != "" {
		opts = append(opts, slack.OptionAPIURL(strings.TrimRight(cfg.SlackBaseURL, "/")+"/"))
	}
	return &Poster{api: slack.New(cfg.BotToken, opts...)}
}

// Message is an outbound chat.postMessage request.
type Message struct {
	Channel  string         // channel ID or name to post into
	Fallback string         // top-level text: notification preview + accessibility fallback
	Blocks   []Block        // Block Kit blocks built with Section/Header/Actions/...
	Metadata map[string]any // when non-nil, attached as message metadata.event_payload and echoed back as Interaction.Metadata on a button click
}

// Post sends msg and returns the posted message's timestamp (ts), usable for a
// later chat.update.
func (p *Poster) Post(ctx context.Context, msg Message) (ts string, err error) {
	opts := []slack.MsgOption{
		slack.MsgOptionText(msg.Fallback, false),
		slack.MsgOptionBlocks(toSlackBlocks(msg.Blocks)...),
	}
	if msg.Metadata != nil {
		opts = append(opts, slack.MsgOptionMetadata(slack.SlackMetadata{
			EventType:    metadataEventType,
			EventPayload: msg.Metadata,
		}))
	}
	_, ts, err = p.api.PostMessageContext(ctx, msg.Channel, opts...)
	return ts, err
}

// toSlackBlocks re-types []Block as the []slack.Block slack-go expects.
func toSlackBlocks(blocks []Block) []slack.Block {
	out := make([]slack.Block, len(blocks))
	for i, b := range blocks {
		out[i] = b
	}
	return out
}
