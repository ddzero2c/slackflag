package slackflag

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	SigningSecret string
	BotToken      string
	HTTPClient    *http.Client
	Logger        *slog.Logger
	SignatureSkew time.Duration
	SlackBaseURL  string
}

const metadataEventType = "slackflag"

type Mux struct {
	cfg      Config
	logger   *slog.Logger
	client   *slackClient
	mu       sync.RWMutex
	commands map[string]*Command
	actions  map[string]ActionFunc
	now      func() time.Time // for tests
}

func NewMux(cfg Config) *Mux {
	if cfg.SigningSecret == "" {
		panic("slackflag.NewMux: empty SigningSecret")
	}
	if cfg.BotToken == "" {
		panic("slackflag.NewMux: empty BotToken")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SignatureSkew == 0 {
		cfg.SignatureSkew = 5 * time.Minute
	}
	if cfg.SlackBaseURL == "" {
		cfg.SlackBaseURL = "https://slack.com/api"
	}
	return &Mux{
		cfg:      cfg,
		logger:   cfg.Logger,
		client:   &slackClient{httpClient: cfg.HTTPClient, baseURL: cfg.SlackBaseURL, botToken: cfg.BotToken},
		commands: map[string]*Command{},
		actions:  map[string]ActionFunc{},
		now:      time.Now,
	}
}

func (m *Mux) Register(cmd *Command) {
	if !strings.HasPrefix(cmd.Name, "/") {
		panic(fmt.Sprintf("slackflag.Register: top-level command name %q must start with /", cmd.Name))
	}
	cmd.validateHandlers()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.commands[cmd.Name]; ok {
		panic(fmt.Sprintf("slackflag: duplicate command %s", cmd.Name))
	}
	m.commands[cmd.Name] = cmd
}

func (m *Mux) lookup(name string) *Command {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.commands[name]
}

// readAndVerify reads the request body and verifies the Slack signature.
// On success returns the raw body bytes.
func (m *Mux) readAndVerify(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	ts := r.Header.Get("X-Slack-Request-Timestamp")
	sig := r.Header.Get("X-Slack-Signature")
	if err := verifySignature(m.cfg.SigningSecret, ts, body, sig, m.now(), m.cfg.SignatureSkew); err != nil {
		return nil, err
	}
	return body, nil
}

func (m *Mux) SlashHandler() http.Handler {
	return http.HandlerFunc(m.serveSlash)
}

func (m *Mux) InteractionHandler() http.Handler {
	return http.HandlerFunc(m.serveInteraction)
}

func (m *Mux) serveSlash(w http.ResponseWriter, r *http.Request) {
	body, err := m.readAndVerify(r)
	if err != nil {
		m.logger.Warn("slash signature invalid", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	cmdName := form.Get("command")
	cmd := m.lookup(cmdName)
	respURL := form.Get("response_url")
	userID := form.Get("user_id")
	userName := form.Get("user_name")
	channelID := form.Get("channel_id")
	text := form.Get("text")

	// Ack 200 immediately; do work asynchronously so we satisfy the 3s SLA.
	w.WriteHeader(http.StatusOK)

	m.asyncRun(r.Context(), "slash:"+cmdName, func() {
		echo := inputEchoBlock(cmdName, text)
		if cmd == nil {
			m.postEphemeral(respURL, []Block{echo, Section(":x: unknown command: " + cmdName)})
			return
		}
		args, terr := tokenize(text)
		if terr != nil {
			m.postEphemeral(respURL, []Block{echo, Section(":x: " + terr.Error())})
			return
		}

		// Walk subcommand path. Branch commands have build == nil; resolve the
		// first positional token to a subcommand until we reach a leaf, then
		// hand the remaining args to the leaf's FlagSet.
		target := cmd
		var subPath []string
		for len(target.subs) > 0 {
			if len(args) == 0 {
				m.renderHelp(respURL, cmd, target, subPath)
				return
			}
			head := args[0]
			if head == "help" || head == "-h" || head == "--help" {
				m.renderHelp(respURL, cmd, target, subPath)
				return
			}
			sub, ok := target.subs[head]
			if !ok {
				m.renderUnknownSub(respURL, echo, cmd, target, subPath, head)
				return
			}
			subPath = append(subPath, head)
			target = sub
			args = args[1:]
		}

		label := cmdLabel(cmd, subPath)
		fs := flag.NewFlagSet(label, flag.ContinueOnError)
		var usageBuf bytes.Buffer
		fs.SetOutput(&usageBuf)
		h := target.build(fs)
		if err := fs.Parse(args); err != nil {
			// fs.Parse writes the error and usage to fs.Output() itself;
			// don't call fs.Usage() again or the message duplicates.
			usage := usageBuf.String()
			if usage == "" {
				usage = err.Error()
			}
			m.postEphemeral(respURL, []Block{echo, Section("```\n" + usage + "\n```")})
			return
		}

		if h.Validate != nil {
			if err := h.Validate(); err != nil {
				m.postEphemeral(respURL, []Block{echo, Section(":x: " + err.Error())})
				return
			}
		}

		ctx := context.Background()
		resp := newResponse()
		safeRun(m.logger, "preview "+label, func() {
			if h.Preview != nil {
				h.Preview(ctx, resp)
			}
		}, resp)

		if resp.failed {
			m.postEphemeral(respURL, append([]Block{echo}, resp.flushBlocks()...))
			return
		}

		if h.Preview == nil {
			// Direct flow: Execute and post in_channel.
			execResp := newResponse()
			safeRun(m.logger, "execute "+label, func() {
				h.Execute(ctx, execResp)
			}, execResp)
			marker := ":white_check_mark:"
			if execResp.failed {
				marker = ":x:"
			}
			footer := Section(fmt.Sprintf("%s ran by <@%s> at %s",
				marker, userName, m.now().Format("2006-01-02 15:04 MST")))
			blocks := append(execResp.flushBlocks(), footer)
			if err := m.client.postResponseURL(ctx, respURL, map[string]any{
				"response_type": "in_channel",
				"blocks":        blocks,
			}); err != nil {
				m.logger.Warn("response_url post failed", "label", "slash:"+label, "err", err)
			}
			return
		}

		// Confirm flow: post preview with Confirm/Cancel buttons + metadata.
		// The button stores the top-level cmd.Name; the subcommand path is
		// recorded in metadata so the interaction handler can walk back to
		// the right *Command.
		md := encodeMetadata(fs, subPath, userID, m.now().UTC().Format(time.RFC3339))
		mdRaw, err := md.marshal()
		if err != nil {
			m.logger.Error("metadata marshal failed", "cmd", label, "err", err)
			m.postEphemeral(respURL, []Block{echo, Section(":x: internal error, please try again")})
			return
		}
		blocks := append(resp.flushBlocks(),
			actionsBlock(cmd.Name),
		)
		if err := m.client.postResponseURL(ctx, respURL, map[string]any{
			"response_type": "in_channel",
			"blocks":        blocks,
			"metadata": map[string]any{
				"event_type":    metadataEventType,
				"event_payload": json.RawMessage(mdRaw),
			},
		}); err != nil {
			m.logger.Warn("response_url post failed", "label", "slash:"+label, "err", err)
		}
		_ = channelID // reserved for chat.postMessage thread reply in interaction flow
	})
}

// postEphemeral pushes an ephemeral followup via response_url.
func (m *Mux) postEphemeral(respURL string, blocks []Block) {
	if respURL == "" {
		return
	}
	if err := m.client.postResponseURL(context.Background(), respURL, map[string]any{
		"response_type": "ephemeral",
		"blocks":        blocks,
	}); err != nil {
		m.logger.Warn("ephemeral post failed", "err", err)
	}
}

// safeRun wraps a handler invocation with panic recovery.
func safeRun(logger *slog.Logger, label string, fn func(), resp *response) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("handler panic", "label", label, "panic", r)
			resp.Fail(fmt.Errorf("panic: %v", r))
		}
	}()
	fn()
}

// actionsBlock returns the Confirm/Cancel buttons for a command. The button
// values carry the top-level command name so the interaction handler can look
// it up.
func actionsBlock(cmdName string) Block {
	return Actions(
		Button("slackflag.confirm", "Confirm", cmdName, "primary"),
		Button("slackflag.cancel", "Cancel", cmdName, "danger"),
	)
}

func (m *Mux) serveInteraction(w http.ResponseWriter, r *http.Request) {
	body, err := m.readAndVerify(r)
	if err != nil {
		m.logger.Warn("interaction signature invalid", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	payloadJSON := form.Get("payload")
	var payload interactionPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	m.asyncRun(r.Context(), "interact", func() {
		if len(payload.Actions) == 0 {
			return
		}
		action := payload.Actions[0]

		// Non-slackflag.* actions skip the confirm/cancel invoker gating below.
		if !strings.HasPrefix(action.ActionID, "slackflag.") {
			if h := m.lookupAction(action.ActionID); h != nil {
				m.dispatchAction(h, payload, parseCallback(payloadJSON))
			} else {
				m.logger.Warn("unknown action_id", "id", action.ActionID)
			}
			return
		}

		md, err := unmarshalMetadata(payload.Message.Metadata.EventPayload)
		if err != nil {
			m.logger.Warn("interaction metadata invalid", "err", err)
			m.postEphemeral(payload.ResponseURL, []Block{
				Section(":x: could not read command metadata, please re-run the command"),
			})
			return
		}

		// only original invoker may confirm or cancel
		if payload.User.ID != md.Invoker {
			m.postEphemeral(payload.ResponseURL, []Block{
				Section(fmt.Sprintf(":lock: only <@%s> can act on this command", md.Invoker)),
			})
			return
		}

		switch action.ActionID {
		case "slackflag.cancel":
			m.finalizePreview(payload.ResponseURL, payload.Message.Blocks,
				fmt.Sprintf(":no_entry_sign: cancelled by <@%s> at %s",
					payload.User.Name, m.now().Format("2006-01-02 15:04 MST")))
			return
		case "slackflag.confirm":
			cmd := m.lookup(action.Value)
			if cmd == nil {
				m.postEphemeral(payload.ResponseURL, []Block{Section(":x: unknown command: " + action.Value)})
				return
			}
			target := cmd
			for _, name := range md.Sub {
				sub, ok := target.subs[name]
				if !ok {
					m.postThread(payload.Channel.ID, payload.Message.TS,
						[]Block{Section(fmt.Sprintf(":x: subcommand %q no longer exists (configuration drift)", name))})
					return
				}
				target = sub
			}
			if target.build == nil {
				m.postThread(payload.Channel.ID, payload.Message.TS,
					[]Block{Section(":x: cannot execute branch command without leaf subcommand")})
				return
			}
			label := cmdLabel(cmd, md.Sub)
			fs := flag.NewFlagSet(label, flag.ContinueOnError)
			h := target.build(fs)
			if err := replayMetadata(fs, md); err != nil {
				m.postThread(payload.Channel.ID, payload.Message.TS,
					[]Block{Section(":x: " + err.Error())})
				return
			}

			// Strip the buttons before running Execute so the invoker can't
			// double-click during the (possibly long) Execute window.
			m.markProcessing(payload.ResponseURL, payload.Message.Blocks, payload.User.Name)

			ctx := context.Background()
			resp := newResponse()
			safeRun(m.logger, "execute "+label, func() {
				h.Execute(ctx, resp)
			}, resp)
			threadBlocks := resp.flushBlocks()
			m.postThread(payload.Channel.ID, payload.Message.TS, threadBlocks)
			if resp.failed {
				// restore the original message (with buttons) so the invoker
				// can retry once they've seen the error in the thread reply
				m.restoreButtons(payload.ResponseURL, payload.Message.Blocks, nil)
				return
			}
			m.finalizePreview(payload.ResponseURL, payload.Message.Blocks,
				fmt.Sprintf(":white_check_mark: executed by <@%s> at %s",
					payload.User.Name, m.now().Format("2006-01-02 15:04 MST")))
		default:
			m.logger.Warn("unknown action_id", "id", action.ActionID)
		}
	})
}

// replaceOriginal updates the source message in place via response_url. blocks
// may be []Block or the []any returned by stripActions; both JSON-marshal
// correctly.
func (m *Mux) replaceOriginal(respURL string, blocks any) {
	if err := m.client.postResponseURL(context.Background(), respURL, map[string]any{
		"replace_original": true,
		"blocks":           blocks,
	}); err != nil {
		m.logger.Warn("replace_original failed", "err", err)
	}
}

// finalizePreview replaces the original preview message: strips actions blocks,
// appends a footer line.
func (m *Mux) finalizePreview(respURL string, original []any, footerText string) {
	m.replaceOriginal(respURL, append(stripActions(original), Section(footerText)))
}

// markProcessing replaces the preview while Execute is in flight: the actions
// block is removed and a transient "running…" footer is appended. This closes
// the double-click window between our 200 OK and the final replace_original
// from finalizePreview / restoreButtons.
func (m *Mux) markProcessing(respURL string, original []any, userName string) {
	footer := Section(fmt.Sprintf(":hourglass_flowing_sand: running by <@%s>…", userName))
	m.replaceOriginal(respURL, append(stripActions(original), footer))
}

// restoreButtons re-posts the original message blocks (including the actions
// row) so a failed Execute or action leaves the user able to retry — undoing
// the strip done by markProcessing. When err is non-nil an error section is
// appended (the action flow surfaces the error inline; the confirm flow passes
// nil because it reports the error in a thread reply instead).
func (m *Mux) restoreButtons(respURL string, original []any, err error) {
	blocks := append([]any{}, original...)
	if err != nil {
		blocks = append(blocks, Section(":x: "+err.Error()))
	}
	m.replaceOriginal(respURL, blocks)
}

// postThread fires a chat.postMessage as a thread reply. Empty blocks are
// dropped: Slack rejects a message with no text and no blocks (no_text), so an
// Execute that produced no output should post nothing rather than error.
func (m *Mux) postThread(channel, threadTS string, blocks []Block) {
	if len(blocks) == 0 {
		return
	}
	if err := m.client.chatPostMessage(context.Background(), channel, threadTS, blocks); err != nil {
		m.logger.Error("chat.postMessage failed", "err", err)
	}
}

// stripActions returns the blocks list without any "actions" blocks. The input
// is the original message's blocks as Slack posted them back to us (decoded
// generically), so each element is a map[string]any; the output stays []any so
// it can be re-sent verbatim with our own Block footers appended.
func stripActions(blocks []any) []any {
	out := make([]any, 0, len(blocks))
	for _, b := range blocks {
		if bm, ok := b.(map[string]any); ok && bm["type"] == "actions" {
			continue
		}
		out = append(out, b)
	}
	return out
}

// asyncRun runs fn in a goroutine with panic recovery, logging panics.
func (m *Mux) asyncRun(ctx context.Context, label string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error("panic in async runner", "label", label, "panic", r)
			}
		}()
		fn()
	}()
}

// cmdLabel returns the human-readable label for a command path,
// e.g. "/admin user-create".
func cmdLabel(top *Command, subPath []string) string {
	if len(subPath) == 0 {
		return top.Name
	}
	return top.Name + " " + strings.Join(subPath, " ")
}

// renderHelp posts an ephemeral listing target's subcommands.
func (m *Mux) renderHelp(respURL string, top *Command, target *Command, subPath []string) {
	var b strings.Builder
	label := cmdLabel(top, subPath)
	if target.Description != "" {
		fmt.Fprintf(&b, "*%s* — %s\n\n", label, target.Description)
	} else {
		fmt.Fprintf(&b, "*%s*\n\n", label)
	}
	b.WriteString("Available subcommands:\n")
	b.WriteString(formatSubList(target))
	m.postEphemeral(respURL, []Block{Section(b.String())})
}

// renderUnknownSub posts an ephemeral when a positional token doesn't match
// any subcommand of target. echo carries the user's original input so they
// can see what they typed alongside the error.
func (m *Mux) renderUnknownSub(respURL string, echo Block, top *Command, target *Command, subPath []string, name string) {
	var b strings.Builder
	fmt.Fprintf(&b, ":x: unknown subcommand `%s`\n\n", name)
	fmt.Fprintf(&b, "Available subcommands for *%s*:\n", cmdLabel(top, subPath))
	b.WriteString(formatSubList(target))
	m.postEphemeral(respURL, []Block{echo, Section(b.String())})
}

// inputEchoBlock returns a Slack mrkdwn blockquote echoing the user's
// original slash invocation, prepended to failure ephemerals so the user
// sees what they typed.
func inputEchoBlock(cmdName, text string) Block {
	if text == "" {
		return Section("> " + cmdName)
	}
	return Section("> " + cmdName + " " + text)
}

// formatSubList renders one line per direct subcommand in registration order.
func formatSubList(target *Command) string {
	var b strings.Builder
	for _, name := range target.subOrder {
		sub := target.subs[name]
		if sub.Description != "" {
			fmt.Fprintf(&b, "• `%s` — %s\n", name, sub.Description)
		} else {
			fmt.Fprintf(&b, "• `%s`\n", name)
		}
	}
	return b.String()
}
