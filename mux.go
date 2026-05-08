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

type Mux struct {
	cfg      Config
	logger   *slog.Logger
	client   *slackClient
	mu       sync.RWMutex
	commands map[string]*Command
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
		if cmd == nil {
			m.postEphemeral(respURL, []Block{Section(":x: unknown command: " + cmdName)})
			return
		}
		args, terr := tokenize(text)
		if terr != nil {
			m.postEphemeral(respURL, []Block{Section(":x: tokenize: " + terr.Error())})
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
				m.renderUnknownSub(respURL, cmd, target, subPath, head)
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
			fs.Usage()
			usage := usageBuf.String()
			if usage == "" {
				usage = err.Error()
			}
			m.postEphemeral(respURL, []Block{Section("```\n" + usage + "\n```")})
			return
		}

		if h.Validate != nil {
			if err := h.Validate(); err != nil {
				m.postEphemeral(respURL, []Block{Section(":x: " + err.Error())})
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
			m.postEphemeral(respURL, resp.flushBlocks())
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
			m.postEphemeral(respURL, []Block{Section(":x: internal error, please try again")})
			return
		}
		blocks := append(resp.flushBlocks(),
			actionsBlock(cmd.Name),
		)
		if err := m.client.postResponseURL(ctx, respURL, map[string]any{
			"response_type": "in_channel",
			"blocks":        blocks,
			"metadata": map[string]any{
				"event_type":    "slackflag",
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

// actionsBlock returns the Confirm/Cancel buttons for a command.
func actionsBlock(cmdName string) Block {
	return map[string]any{
		"type": "actions",
		"elements": []map[string]any{
			{
				"type":      "button",
				"text":      map[string]any{"type": "plain_text", "text": "Confirm"},
				"style":     "primary",
				"action_id": "slackflag.confirm",
				"value":     cmdName,
			},
			{
				"type":      "button",
				"text":      map[string]any{"type": "plain_text", "text": "Cancel"},
				"style":     "danger",
				"action_id": "slackflag.cancel",
				"value":     cmdName,
			},
		},
	}
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
	var payload struct {
		Type    string `json:"type"`
		User    struct{ ID, Name string } `json:"user"`
		Channel struct{ ID string }       `json:"channel"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
		Message struct {
			TS       string          `json:"ts"`
			Blocks   []any           `json:"blocks"`
			Metadata struct {
				EventType    string          `json:"event_type"`
				EventPayload json.RawMessage `json:"event_payload"`
			} `json:"metadata"`
		} `json:"message"`
		ResponseURL string `json:"response_url"`
	}
	if err := json.Unmarshal([]byte(form.Get("payload")), &payload); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	m.asyncRun(r.Context(), "interact", func() {
		if len(payload.Actions) == 0 {
			return
		}
		action := payload.Actions[0]
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
			ctx := context.Background()
			resp := newResponse()
			safeRun(m.logger, "execute "+label, func() {
				h.Execute(ctx, resp)
			}, resp)
			threadBlocks := resp.flushBlocks()
			m.postThread(payload.Channel.ID, payload.Message.TS, threadBlocks)
			if resp.failed {
				// keep button alive — do NOT replace_original
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

// finalizePreview replaces the original preview message: strips actions blocks,
// appends a footer line.
func (m *Mux) finalizePreview(respURL string, original []any, footerText string) {
	stripped := stripActions(original)
	stripped = append(stripped, Section(footerText))
	if err := m.client.postResponseURL(context.Background(), respURL, map[string]any{
		"replace_original": true,
		"blocks":           stripped,
	}); err != nil {
		m.logger.Warn("finalize preview failed", "err", err)
	}
}

// postThread fires a chat.postMessage as a thread reply.
func (m *Mux) postThread(channel, threadTS string, blocks []Block) {
	if err := m.client.chatPostMessage(context.Background(), channel, threadTS, blocks); err != nil {
		m.logger.Error("chat.postMessage failed", "err", err)
	}
}

// stripActions returns the blocks list without any "actions" blocks.
func stripActions(blocks []any) []Block {
	out := make([]Block, 0, len(blocks))
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
// any subcommand of target.
func (m *Mux) renderUnknownSub(respURL string, top *Command, target *Command, subPath []string, name string) {
	var b strings.Builder
	fmt.Fprintf(&b, ":x: unknown subcommand `%s`\n\n", name)
	fmt.Fprintf(&b, "Available subcommands for *%s*:\n", cmdLabel(top, subPath))
	b.WriteString(formatSubList(target))
	m.postEphemeral(respURL, []Block{Section(b.String())})
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
