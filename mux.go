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
		fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
		var usageBuf bytes.Buffer
		fs.SetOutput(&usageBuf)
		h := cmd.build(fs)
		args, terr := tokenize(text)
		if terr != nil {
			m.postEphemeral(respURL, []Block{Section(":x: tokenize: " + terr.Error())})
			return
		}
		if err := fs.Parse(args); err != nil {
			fs.Usage()
			usage := usageBuf.String()
			if usage == "" {
				usage = err.Error()
			}
			m.postEphemeral(respURL, []Block{Section("```\n" + usage + "\n```")})
			return
		}

		ctx := context.Background()
		resp := newResponse()
		safeRun(m.logger, "preview "+cmd.Name, func() {
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
			safeRun(m.logger, "execute "+cmd.Name, func() {
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
				m.logger.Warn("response_url post failed", "label", "slash:"+cmd.Name, "err", err)
			}
			return
		}

		// Confirm flow: post preview with Confirm/Cancel buttons + metadata.
		md := encodeMetadata(fs, userID, m.now().UTC().Format(time.RFC3339))
		mdRaw, _ := md.marshal()
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
			m.logger.Warn("response_url post failed", "label", "slash:"+cmd.Name, "err", err)
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
		md, _ := unmarshalMetadata(payload.Message.Metadata.EventPayload)

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
			fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
			h := cmd.build(fs)
			if err := replayMetadata(fs, md); err != nil {
				m.postThread(payload.Channel.ID, payload.Message.TS,
					[]Block{Section(":x: " + err.Error())})
				return
			}
			ctx := context.Background()
			resp := newResponse()
			safeRun(m.logger, "execute "+cmd.Name, func() {
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
