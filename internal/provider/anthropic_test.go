package provider

import (
	"context"
	"encoding/json"
	"io"
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

func TestAnthropicChatStream(t *testing.T) {
	// Authentic Anthropic SSE: input_tokens arrive on message_start, the final
	// (cumulative) output_tokens on message_delta.
	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4-5-20250101\",\"usage\":{\"input_tokens\":11,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" there\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got anthropicReq
		_ = json.NewDecoder(r.Body).Decode(&got)
		if !got.Stream {
			t.Errorf("stream request must set stream:true: %+v", got)
		}
		if r.Header.Get("accept") != "text/event-stream" {
			t.Errorf("accept = %q, want text/event-stream", r.Header.Get("accept"))
		}
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("missing x-api-key: %q", r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	a := NewAnthropic("k")
	a.baseURL = srv.URL
	st, err := a.ChatStream(context.Background(), Request{Model: "claude-sonnet-4-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer st.Close()

	var forwarded strings.Builder
	for {
		chunk, err := st.Recv()
		forwarded.Write(chunk)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}
	// The client receives Anthropic's SSE verbatim.
	if forwarded.String() != sse {
		t.Errorf("forwarded stream != upstream:\n got %q\nwant %q", forwarded.String(), sse)
	}
	// Usage is assembled from the stream: input from message_start, the final
	// output from message_delta (overriding message_start's initial 1).
	if u := st.Usage(); u.PromptTokens != 11 || u.CompletionTokens != 7 {
		t.Errorf("usage = %+v, want 11/7", u)
	}
	if st.Model() != "claude-sonnet-4-5-20250101" {
		t.Errorf("model = %q, want claude-sonnet-4-5-20250101", st.Model())
	}
}

func TestAnthropicChatStream_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad model"}}`))
	}))
	defer srv.Close()

	a := NewAnthropic("k")
	a.baseURL = srv.URL
	_, err := a.ChatStream(context.Background(), Request{Model: "x", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "bad model") {
		t.Fatalf("expected API error surfaced, got %v", err)
	}
}

func TestAnthropicChatStream_MissingKey(t *testing.T) {
	a := NewAnthropic("")
	_, err := a.ChatStream(context.Background(), Request{Model: "x", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}
