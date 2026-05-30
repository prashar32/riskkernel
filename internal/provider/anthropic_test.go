package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicChat_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing/wrong x-api-key: %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != anthropicAPIVersion {
			t.Errorf("wrong anthropic-version: %q", r.Header.Get("anthropic-version"))
		}
		var got anthropicReq
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got.System != "be terse" {
			t.Errorf("system not lifted from messages: %q", got.System)
		}
		if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
			t.Errorf("unexpected messages: %+v", got.Messages)
		}
		if got.MaxTokens != 256 {
			t.Errorf("max_tokens = %d, want 256", got.MaxTokens)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_123","model":"claude-sonnet-4-5-20250101","stop_reason":"end_turn",
			"content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}],
			"usage":{"input_tokens":11,"output_tokens":4}
		}`))
	}))
	defer srv.Close()

	a := NewAnthropic("test-key")
	a.baseURL = srv.URL

	resp, err := a.Chat(context.Background(), Request{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 256,
		Messages: []Message{
			{Role: RoleSystem, Content: "be terse"},
			{Role: RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hello world" {
		t.Errorf("content = %q, want %q", resp.Content, "hello world")
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 4 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Usage.Total() != 15 {
		t.Errorf("total = %d, want 15", resp.Usage.Total())
	}
	if resp.FinishReason != "end_turn" {
		t.Errorf("finish = %q", resp.FinishReason)
	}
}

func TestAnthropicChat_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad model"}}`))
	}))
	defer srv.Close()

	a := NewAnthropic("k")
	a.baseURL = srv.URL
	_, err := a.Chat(context.Background(), Request{Model: "x", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "bad model") {
		t.Fatalf("expected API error surfaced, got %v", err)
	}
}

func TestAnthropicChat_MissingKey(t *testing.T) {
	a := NewAnthropic("")
	_, err := a.Chat(context.Background(), Request{Model: "x", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}

func TestAnthropicChat_ContextCancel(t *testing.T) {
	a := NewAnthropic("k")
	a.baseURL = "http://127.0.0.1:0" // unused; cancel before dial
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.Chat(ctx, Request{Model: "x", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
