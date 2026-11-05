// Package standin is a local stand-in for a chat model. Copilot Ready points one offline Copilot
// turn at it so Copilot adds an imported conversation to its session index without calling a real
// model: no AI credits are used and nothing leaves the computer.
package standin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Server answers OpenAI-compatible chat completions on a random loopback port. It accepts only
// requests that carry its per-run key and come from this computer. Every answer is the same
// short message, and it never asks Copilot to run a tool.
type Server struct {
	URL string // for COPILOT_PROVIDER_BASE_URL
	Key string // for COPILOT_PROVIDER_API_KEY
	srv *http.Server
}

func Start(reply string) (*Server, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{URL: "http://" + ln.Addr().String() + "/v1", Key: hex.EncodeToString(key)}
	s.srv = &http.Server{Handler: Handler(s.Key, reply), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Close stops listening and drops open connections.
func (s *Server) Close() error {
	if err := s.srv.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func Handler(key, reply string) http.Handler {
	want := []byte("Bearer " + key)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			http.Error(w, "local requests only", http.StatusForbidden)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		// The request carries the whole conversation. Only the two fields the answer needs are
		// kept; the body is never logged or stored.
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&req) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		created := time.Now().Unix()
		usage := map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "chatcmpl-copilot-ready", "object": "chat.completion", "created": created, "model": req.Model,
				"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}}, "usage": usage})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		chunk := func(choices []any, extra map[string]any) {
			v := map[string]any{"id": "chatcmpl-copilot-ready", "object": "chat.completion.chunk", "created": created, "model": req.Model, "choices": choices}
			for k, x := range extra {
				v[k] = x
			}
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		chunk([]any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": reply}, "finish_reason": nil}}, nil)
		chunk([]any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, nil)
		chunk([]any{}, map[string]any{"usage": usage})
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
}
