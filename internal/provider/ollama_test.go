package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaChat_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", r.URL.Path)
		}
		var got ollamaReq
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got.Stream {
			t.Error("stream should be false")
		}
		// System prompt should be the leading system message.
		if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[0].Content != "be terse" {
			t.Errorf("system not prepended: %+v", got.Messages)
		}
		if got.Options == nil || got.Options.NumPredict != 64 {
			t.Errorf("options not mapped: %+v", got.Options)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"qwen2.5-coder",
			"message":{"role":"assistant","content":"hello from local"},
			"done":true,"done_reason":"stop",
			"prompt_eval_count":18,"eval_count":5
		}`))
	}))
	defer srv.Close()

	o := NewOllama(srv.URL)
	resp, err := o.Chat(context.Background(), Request{
		Model:     "qwen2.5-coder",
		System:    "be terse",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hello from local" || resp.FinishReason != "stop" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.PromptTokens != 18 || resp.Usage.CompletionTokens != 5 || resp.Usage.Total() != 23 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Model != "qwen2.5-coder" {
		t.Errorf("model = %q", resp.Model)
	}
}

func TestOllamaChat_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model \"ghost\" not found, try pulling it first"}`))
	}))
	defer srv.Close()

	o := NewOllama(srv.URL)
	_, err := o.Chat(context.Background(), Request{Model: "ghost", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error surfaced, got %v", err)
	}
}

func TestOllamaDefaultBaseURL(t *testing.T) {
	if o := NewOllama(""); o.baseURL != defaultOllamaBaseURL {
		t.Errorf("empty baseURL = %q, want default %q", o.baseURL, defaultOllamaBaseURL)
	}
	if o := NewOllama("http://remote:11434/"); o.baseURL != "http://remote:11434" {
		t.Errorf("trailing slash not trimmed: %q", o.baseURL)
	}
}
