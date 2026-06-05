package slackflag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Interaction is the context passed to an ActionFunc when a routed button is
// clicked. It carries everything a handler typically needs without exposing the
// raw Slack payload.
type Interaction struct {
	ActionID  string         // the clicked button's action_id
	Value     string         // the button's value (caller context, e.g. an order UUID)
	UserID    string         // the clicking user's ID
	UserName  string         // the clicking user's name
	Channel   string         // channel the message lives in
	MessageTS string         // the message's ts, for a later chat.update
	Metadata  map[string]any // the message's metadata.event_payload, as set on Poster.Post
}

// ActionFunc handles a routed button click. Blocks written to w replace the
// original message; returning a non-nil error (or calling w.Fail) restores the
// original message with the buttons intact plus an error note so the user can
// retry.
//
// Slack expects an interaction acknowledged within 3 seconds. slackflag acks
// immediately and runs the handler asynchronously, but the replace_original
// update still reflects whatever the handler does synchronously. For long work,
// hand it off to your own queue and write a "processing…" block here.
type ActionFunc func(ctx context.Context, ic Interaction, w Response) error

// HandleAction registers h for an exact action_id, used to route button clicks
// on proactive messages (see Poster). It panics if actionID uses the reserved
// "slackflag." prefix or is already registered. Returns m for chaining.
func (m *Mux) HandleAction(actionID string, h ActionFunc) *Mux {
	if strings.HasPrefix(actionID, "slackflag.") {
		panic(fmt.Sprintf("slackflag.HandleAction: action_id %q uses the reserved slackflag. prefix", actionID))
	}
	if h == nil {
		panic("slackflag.HandleAction: nil handler")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.actions[actionID]; ok {
		panic(fmt.Sprintf("slackflag.HandleAction: duplicate action_id %q", actionID))
	}
	m.actions[actionID] = h
	return m
}

func (m *Mux) lookupAction(id string) ActionFunc {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.actions[id]
}

// interactionPayload is the subset of Slack's block_actions payload we read. It
// is decoded generically (Message.Blocks stays []any) so the original message
// can be re-sent verbatim with stripActions.
type interactionPayload struct {
	Type    string                    `json:"type"`
	User    struct{ ID, Name string } `json:"user"`
	Channel struct{ ID string }       `json:"channel"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	Message struct {
		TS       string `json:"ts"`
		Blocks   []any  `json:"blocks"`
		Metadata struct {
			EventType    string          `json:"event_type"`
			EventPayload json.RawMessage `json:"event_payload"`
		} `json:"metadata"`
	} `json:"message"`
	ResponseURL string `json:"response_url"`
}

// dispatchAction runs a HandleAction handler and updates the original message.
// It mirrors the confirm/cancel lifecycle: strip the buttons immediately to
// close the double-click window, run the handler, then replace the message with
// the handler's blocks (success) or restore the buttons plus an error (failure).
func (m *Mux) dispatchAction(h ActionFunc, p interactionPayload) {
	action := p.Actions[0]
	original := p.Message.Blocks

	m.markProcessing(p.ResponseURL, original, p.User.Name)

	var meta map[string]any
	if len(p.Message.Metadata.EventPayload) > 0 {
		if err := json.Unmarshal(p.Message.Metadata.EventPayload, &meta); err != nil {
			m.logger.Warn("action metadata decode failed", "id", action.ActionID, "err", err)
		}
	}

	ic := Interaction{
		ActionID:  action.ActionID,
		Value:     action.Value,
		UserID:    p.User.ID,
		UserName:  p.User.Name,
		Channel:   p.Channel.ID,
		MessageTS: p.Message.TS,
		Metadata:  meta,
	}

	resp := newResponse()
	safeRun(m.logger, "action "+action.ActionID, func() {
		if err := h(context.Background(), ic, resp); err != nil {
			resp.Fail(err)
		}
	}, resp)

	if resp.failed {
		m.restoreButtons(p.ResponseURL, original, resp.failErr)
		return
	}

	blocks := resp.flushBlocks()
	if len(blocks) == 0 {
		m.replaceOriginal(p.ResponseURL, stripActions(original))
		return
	}
	m.replaceOriginal(p.ResponseURL, blocks)
}
