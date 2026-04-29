package mockslack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Server captures incoming Slack API and response_url POSTs for assertion.
type Server struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	calls   []Call
	respURL string
}

type Call struct {
	URL    string         // either "/api/chat.postMessage" or "/response/<id>"
	Body   map[string]any // decoded JSON
	Header http.Header
}

// New starts an httptest.Server that mocks both Slack Web API endpoints
// (chat.postMessage) and a single response_url endpoint.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat.postMessage", s.handleAPI)
	mux.HandleFunc("/response/", s.handleResponseURL)
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	s.respURL = s.server.URL + "/response/test"
	return s
}

func (s *Server) URL() string         { return s.server.URL + "/api" }
func (s *Server) ResponseURL() string { return s.respURL }

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	s.mu.Lock()
	s.calls = append(s.calls, Call{URL: r.URL.Path, Body: decoded, Header: r.Header.Clone()})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleResponseURL(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	s.mu.Lock()
	s.calls = append(s.calls, Call{URL: r.URL.Path, Body: decoded, Header: r.Header.Clone()})
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// Calls returns a snapshot of received calls.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, len(s.calls))
	copy(out, s.calls)
	return out
}

// WaitFor polls until at least n calls have been received or timeout elapses.
// Useful because Mux dispatches handler logic on a goroutine.
func (s *Server) WaitFor(n int, timeout time.Duration) []Call {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c := s.Calls(); len(c) >= n {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.t.Fatalf("timed out waiting for %d calls; got %d", n, len(s.Calls()))
	return nil
}

// SignedSlashRequest builds an *http.Request mimicking a Slack slash command
// signed with the given secret.
func SignedSlashRequest(t *testing.T, secret, command, text, userID, userName, channelID, responseURL string) *http.Request {
	t.Helper()
	form := url.Values{}
	form.Set("token", "irrelevant")
	form.Set("command", command)
	form.Set("text", text)
	form.Set("user_id", userID)
	form.Set("user_name", userName)
	form.Set("channel_id", channelID)
	form.Set("team_id", "T1")
	form.Set("response_url", responseURL)
	form.Set("trigger_id", "tr1")
	body := form.Encode()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/slack/command", nil)
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

// SignedInteractionRequest builds an *http.Request mimicking a Slack
// block_actions interaction.
func SignedInteractionRequest(t *testing.T, secret string, payload map[string]any) *http.Request {
	t.Helper()
	pj, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	form.Set("payload", string(pj))
	body := form.Encode()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/slack/interact", nil)
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

// Reset clears recorded calls.
func (s *Server) Reset() {
	s.mu.Lock()
	s.calls = nil
	s.mu.Unlock()
}
