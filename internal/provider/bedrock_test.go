package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const bedrockModel = "anthropic.claude-3-5-sonnet-20240620-v1:0"

func TestBedrockChat_Success(t *testing.T) {
	var gotPath, gotAuth, gotDate, gotCT string
	var gotBody bedrockReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.RequestURI
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("X-Amz-Date")
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("x-amzn-RequestId", "req-123")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"output":{"message":{"role":"assistant","content":[{"text":"hello "},{"text":"world"}]}},
			"stopReason":"end_turn",
			"usage":{"inputTokens":11,"outputTokens":4,"totalTokens":15}
		}`))
	}))
	defer srv.Close()

	b := NewBedrock("AKID", "secret", "", "us-east-1").WithBaseURL(srv.URL)
	resp, err := b.Chat(context.Background(), Request{
		Model:     bedrockModel,
		System:    "be terse",
		MaxTokens: 256,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// The model id's ':' is percent-encoded in the path that's actually sent (so it
	// matches the signed canonical URI).
	if gotPath != "/model/anthropic.claude-3-5-sonnet-20240620-v1%3A0/converse" {
		t.Errorf("request path = %q", gotPath)
	}
	if gotCT != "application/json" || gotDate == "" {
		t.Errorf("headers: content-type=%q x-amz-date=%q", gotCT, gotDate)
	}
	// A well-formed SigV4 Authorization scoped to the bedrock service, signing the
	// content-type, host, and date headers.
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKID/") ||
		!strings.Contains(gotAuth, "/us-east-1/bedrock/aws4_request") ||
		!strings.Contains(gotAuth, "SignedHeaders=content-type;host;x-amz-date") ||
		!strings.Contains(gotAuth, "Signature=") {
		t.Errorf("Authorization not well-formed: %q", gotAuth)
	}
	// System lifted out; the user message mapped to a Converse content block.
	if len(gotBody.System) != 1 || gotBody.System[0].Text != "be terse" {
		t.Errorf("system = %+v", gotBody.System)
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Role != "user" ||
		len(gotBody.Messages[0].Content) != 1 || gotBody.Messages[0].Content[0].Text != "hi" {
		t.Errorf("messages = %+v", gotBody.Messages)
	}
	if gotBody.InferenceConfig == nil || gotBody.InferenceConfig.MaxTokens != 256 {
		t.Errorf("inferenceConfig = %+v", gotBody.InferenceConfig)
	}

	if resp.Content != "hello world" || resp.FinishReason != "end_turn" || resp.Model != bedrockModel {
		t.Errorf("resp = %+v", resp)
	}
	if resp.ID != "req-123" {
		t.Errorf("id = %q, want req-123 (from x-amzn-RequestId)", resp.ID)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 4 || resp.Usage.Total() != 15 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestBedrockChat_SessionToken(t *testing.T) {
	var gotToken, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Amz-Security-Token")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"output":{"message":{"content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1}}`))
	}))
	defer srv.Close()

	b := NewBedrock("AKID", "secret", "session-tok", "us-west-2").WithBaseURL(srv.URL)
	if _, err := b.Chat(context.Background(), Request{Model: bedrockModel, Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotToken != "session-tok" {
		t.Errorf("x-amz-security-token = %q", gotToken)
	}
	// The session token must be part of the signed headers (some services require it).
	if !strings.Contains(gotAuth, "x-amz-security-token") {
		t.Errorf("session token not in SignedHeaders: %q", gotAuth)
	}
}

func TestBedrockChat_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"The provided model identifier is invalid."}`))
	}))
	defer srv.Close()

	b := NewBedrock("AKID", "secret", "", "us-east-1").WithBaseURL(srv.URL)
	_, err := b.Chat(context.Background(), Request{Model: "bad", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "provided model identifier is invalid") {
		t.Fatalf("expected API error surfaced, got %v", err)
	}
}

func TestBedrockChat_MissingCreds(t *testing.T) {
	b := NewBedrock("", "", "", "us-east-1")
	_, err := b.Chat(context.Background(), Request{Model: bedrockModel, Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "missing AWS credentials") {
		t.Fatalf("expected missing-credentials error, got %v", err)
	}
}

func TestBedrockChat_MissingRegion(t *testing.T) {
	b := NewBedrock("AKID", "secret", "", "")
	_, err := b.Chat(context.Background(), Request{Model: bedrockModel, Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "missing AWS region") {
		t.Fatalf("expected missing-region error, got %v", err)
	}
}

func TestBedrockChat_ContextCancel(t *testing.T) {
	b := NewBedrock("AKID", "secret", "", "us-east-1").WithBaseURL("http://127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := b.Chat(ctx, Request{Model: bedrockModel, Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
