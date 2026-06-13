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

func TestOpenAIChat_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer test-key" {
			t.Errorf("missing/wrong auth header: %q", r.Header.Get("authorization"))
		}
		var got openAIReq
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		// System prompt should be the leading system message.
		if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[0].Content != "be terse" {
			t.Errorf("system not prepended: %+v", got.Messages)
		}
		if got.Messages[1].Role != "user" {
			t.Errorf("unexpected messages: %+v", got.Messages)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1","model":"gpt-4o-2024",
			"choices":[{"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}
		}`))
	}))
	defer srv.Close()

	o := NewOpenAI("test-key")
	o.baseURL = srv.URL

	resp, err := o.Chat(context.Background(), Request{
		Model:    "gpt-4o",
		System:   "be terse",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hi there" || resp.FinishReason != "stop" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 3 || resp.Usage.Total() != 15 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Model != "gpt-4o-2024" {
		t.Errorf("model = %q", resp.Model)
	}
}

func TestProviderWithBaseURL(t *testing.T) {
	// Field logic: trailing slash trimmed; empty keeps the default.
	if got := NewOpenAI("k").WithBaseURL("http://x/").baseURL; got != "http://x" {
		t.Errorf("OpenAI WithBaseURL trim = %q", got)
	}
	if got := NewOpenAI("k").WithBaseURL("").baseURL; got != defaultOpenAIBaseURL {
		t.Errorf("OpenAI empty override = %q, want default", got)
	}
	if got := NewAnthropic("k").WithBaseURL("http://y/").baseURL; got != "http://y" {
		t.Errorf("Anthropic WithBaseURL trim = %q", got)
	}

	// End-to-end: a Chat call routes to the overridden base (a mock here), not the
	// default endpoint — this is what lets the benchmark point at a local provider.
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`))
	}))
	defer srv.Close()

	resp, err := NewOpenAI("k").WithBaseURL(srv.URL).Chat(context.Background(),
		Request{Model: "gpt-4o", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat via override: %v", err)
	}
	if !hit {
		t.Error("call did not route to the overridden base URL")
	}
	if resp.Content != "ok" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestOpenAIChat_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit","type":"rate_limit_error","code":"429"}}`))
	}))
	defer srv.Close()

	o := NewOpenAI("k")
	o.baseURL = srv.URL
	_, err := o.Chat(context.Background(), Request{Model: "gpt-4o", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("expected API error surfaced, got %v", err)
	}
}

func TestOpenAIChat_MissingKey(t *testing.T) {
	o := NewOpenAI("")
	_, err := o.Chat(context.Background(), Request{Model: "gpt-4o", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}

func TestOpenAIChat_ContextCancel(t *testing.T) {
	o := NewOpenAI("k")
	o.baseURL = "http://127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := o.Chat(ctx, Request{Model: "gpt-4o", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestOpenAIChatStream(t *testing.T) {
	sse := "data: {\"model\":\"gpt-4o-2024\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n" +
		"data: {\"model\":\"gpt-4o-2024\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got openAIReq
		_ = json.NewDecoder(r.Body).Decode(&got)
		if !got.Stream || got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
			t.Errorf("stream request must set stream + include_usage: %+v", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	o := NewOpenAI("k")
	o.baseURL = srv.URL
	st, err := o.ChatStream(context.Background(), Request{Model: "gpt-4o", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
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
	// The client receives the provider's SSE verbatim.
	if forwarded.String() != sse {
		t.Errorf("forwarded stream != upstream:\n got %q\nwant %q", forwarded.String(), sse)
	}
	// Usage and model are extracted from the stream for metering.
	if u := st.Usage(); u.PromptTokens != 12 || u.CompletionTokens != 3 {
		t.Errorf("usage = %+v, want 12/3", u)
	}
	if st.Model() != "gpt-4o-2024" {
		t.Errorf("model = %q, want gpt-4o-2024", st.Model())
	}
}
