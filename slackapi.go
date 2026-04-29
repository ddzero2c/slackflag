package slackflag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type slackClient struct {
	httpClient *http.Client
	baseURL    string // default https://slack.com/api
	botToken   string
}

// postResponseURL POSTs the given JSON-serializable body to a Slack response_url.
// Used for slash command followups and updating the original message after interactions.
func (c *slackClient) postResponseURL(ctx context.Context, url string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("response_url POST: %d: %s", resp.StatusCode, b)
	}
	return nil
}

// chatPostMessage posts a thread reply via Slack Web API chat.postMessage.
// Requires BotToken on the Mux Config.
func (c *slackClient) chatPostMessage(ctx context.Context, channel, threadTS string, blocks []Block) error {
	body := map[string]any{
		"channel":   channel,
		"thread_ts": threadTS,
		"blocks":    blocks,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat.postMessage", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.botToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chat.postMessage: %d: %s", resp.StatusCode, b)
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("chat.postMessage: %s", result.Error)
	}
	return nil
}
