// Package provider defines RiskKernel's LLM provider abstraction. A Provider is
// the only place in the codebase (besides internal/otel) permitted to make
// outbound network calls. Each Chat call returns token Usage so the deterministic
// governor and cost ledger can attribute spend to a run.
//
// Anthropic, OpenAI, Ollama, and AWS Bedrock are implemented natively. The
// interface is intentionally provider-neutral so the gateway and governor never
// special-case a vendor; the long tail is fronted via LiteLLM (see docs).
package provider

import (
	"context"
)

// Role identifies the author of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a single turn in a conversation.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Request is a provider-neutral chat completion request.
type Request struct {
	// Model is the provider-specific model identifier (e.g. "claude-sonnet-4-5").
	Model string `json:"model"`
	// System is an optional system prompt, kept separate from Messages because
	// some providers (Anthropic) take it as a distinct field.
	System string `json:"system,omitempty"`
	// Messages is the ordered conversation, excluding the system prompt.
	Messages []Message `json:"messages"`
	// MaxTokens caps the completion length. Required by some providers
	// (Anthropic); a provider may supply a sane default when zero.
	MaxTokens int `json:"max_tokens,omitempty"`
	// Temperature is optional; nil means "use the provider default".
	Temperature *float64 `json:"temperature,omitempty"`
}

// Usage reports tokens consumed by a single call. This is the unit the governor
// meters and the cost ledger prices.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

// Total returns the sum of prompt and completion tokens.
func (u Usage) Total() int64 { return u.PromptTokens + u.CompletionTokens }

// Response is a provider-neutral chat completion result.
type Response struct {
	// ID is the provider's response identifier, for the OTel span and audit log.
	ID string `json:"id"`
	// Model is the model that actually served the request.
	Model string `json:"model"`
	// Content is the assistant's text output (concatenated across content blocks).
	Content string `json:"content"`
	// FinishReason is the provider's stop reason, normalized loosely
	// ("stop", "length", "tool_use", ...).
	FinishReason string `json:"finish_reason"`
	// Usage is the token accounting for this call.
	Usage Usage `json:"usage"`
}

// Provider is an LLM backend. Implementations must be safe for concurrent use.
type Provider interface {
	// Name is the stable provider identifier used in config, routing, and the
	// OTel gen_ai.system attribute (e.g. "anthropic").
	Name() string
	// Chat performs one chat completion. It must honor ctx for cancellation and
	// deadlines — this is how the governor's kill switch and time budget reach an
	// in-flight call. It must always populate Usage on success.
	Chat(ctx context.Context, req Request) (*Response, error)
}
