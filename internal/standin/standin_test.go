package standin

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const reply = "Ready to continue."

// request is shaped like Copilot 1.0.105's: it offers tools and asks for usage in the stream.
func request(stream bool) string {
	b, _ := json.Marshal(map[string]any{"model": "auto", "stream": stream, "stream_options": map[string]any{"include_usage": true},
		"messages":    []any{map[string]any{"role": "user", "content": "Imported from Claude Code by Copilot Ready."}},
		"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "bash", "parameters": map[string]any{"type": "object"}}}},
		"tool_choice": "required"})
	return string(b)
}

func post(t *testing.T, s *Server, key, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", s.URL+"/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestListensOnLoopbackWithAFreshKey(t *testing.T) {
	a, err := Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	u, _ := url.Parse(a.URL)
	if host, port, _ := net.SplitHostPort(u.Host); host != "127.0.0.1" || port == "0" || u.Path != "/v1" {
		t.Fatalf("listens on %s", a.URL)
	}
	if len(a.Key) != 64 || a.Key == b.Key || a.URL == b.URL {
		t.Fatalf("keys and ports must be random per run: %s %s", a.URL, b.URL)
	}
}

func TestRefusesAMissingOrWrongKey(t *testing.T) {
	s, err := Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, key := range []string{"", "wrong", s.Key + "x", strings.ToUpper(s.Key)} {
		if resp := post(t, s, key, request(false)); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("key %q got %d", key, resp.StatusCode)
		}
	}
	if resp := post(t, s, s.Key, request(false)); resp.StatusCode != http.StatusOK {
		t.Fatalf("right key got %d", resp.StatusCode)
	}
}

func TestRefusesRequestsFromAnotherComputer(t *testing.T) {
	h := Handler("k", reply)
	for _, remote := range []string{"192.168.1.20:5000", "[2001:db8::1]:5000", "garbage"} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(request(false)))
		r.RemoteAddr = remote
		r.Header.Set("Authorization", "Bearer k")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s got %d", remote, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/v1/models", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("Authorization", "Bearer k")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("only chat completions are served, got %d", w.Code)
	}
}

func TestAnswersWithTheFixedReplyAndNeverAToolCall(t *testing.T) {
	s, err := Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var whole struct {
		Model   string
		Choices []struct {
			Message      map[string]any
			FinishReason string `json:"finish_reason"`
		}
	}
	resp := post(t, s, s.Key, request(false))
	if err := json.NewDecoder(resp.Body).Decode(&whole); err != nil {
		t.Fatal(err)
	}
	if whole.Model != "auto" || len(whole.Choices) != 1 || whole.Choices[0].Message["content"] != reply || whole.Choices[0].FinishReason != "stop" || whole.Choices[0].Message["tool_calls"] != nil {
		t.Fatalf("unexpected answer %+v", whole)
	}

	resp = post(t, s, s.Key, request(true))
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream content type %q", resp.Header.Get("Content-Type"))
	}
	var text strings.Builder
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			lines = append(lines, line)
		}
	}
	if len(lines) < 2 || lines[len(lines)-1] != "[DONE]" {
		t.Fatalf("stream must end with [DONE]: %q", lines)
	}
	usage := false
	for _, line := range lines[:len(lines)-1] {
		var c struct {
			Choices []struct {
				Delta map[string]any
			}
			Usage map[string]any
		}
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatal(err)
		}
		usage = usage || c.Usage != nil
		for _, ch := range c.Choices {
			if ch.Delta["tool_calls"] != nil {
				t.Fatal("the stand-in must never ask for a tool")
			}
			if s, ok := ch.Delta["content"].(string); ok {
				text.WriteString(s)
			}
		}
	}
	if text.String() != reply || !usage {
		t.Fatalf("streamed %q, usage %v", text.String(), usage)
	}
}

func TestCloseStopsListening(t *testing.T) {
	s, err := Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(s.URL)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if c, err := net.DialTimeout("tcp", u.Host, time.Second); err == nil {
		c.Close()
		t.Fatal("the port is still open after Close")
	}
}
