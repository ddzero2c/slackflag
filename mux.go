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
			_ = m.client.postResponseURL(ctx, respURL, map[string]any{
				"response_type": "in_channel",
				"blocks":        blocks,
			})
			return
		}

		// Confirm flow: post preview with Confirm/Cancel buttons + metadata.
		md := encodeMetadata(fs, userID, m.now().UTC().Format(time.RFC3339))
		mdRaw, _ := md.marshal()
		blocks := append(resp.flushBlocks(),
			actionsBlock(cmd.Name),
		)
		_ = m.client.postResponseURL(ctx, respURL, map[string]any{
			"response_type": "in_channel",
			"blocks":        blocks,
			"metadata": map[string]any{
				"event_type":    "slackflag",
				"event_payload": json.RawMessage(mdRaw),
			},
		})
		_ = channelID // reserved for future use; not needed for response_url path
		_ = userName
	})
}

// postEphemeral pushes an ephemeral followup via response_url.
func (m *Mux) postEphemeral(respURL string, blocks []Block) {
	if respURL == "" {
		return
	}
	_ = m.client.postResponseURL(context.Background(), respURL, map[string]any{
		"response_type": "ephemeral",
		"blocks":        blocks,
	})
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
	_, err := m.readAndVerify(r)
	if err != nil {
		m.logger.Warn("interaction signature invalid", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// TODO: filled in Task 12+
	w.WriteHeader(http.StatusOK)
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
