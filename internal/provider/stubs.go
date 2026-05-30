package provider

import "context"

// The providers below are stubs for v0.1. They satisfy the Provider interface so
// routing and config can reference them, but Chat returns ErrNotImplemented until
// each is built out. For the long tail of providers, the documented path is to
// front RiskKernel with LiteLLM rather than reimplement 100+ vendors here.
// (Anthropic and OpenAI are implemented natively — see anthropic.go, openai.go.)

// Bedrock is a stub. Native implementation is planned post-v0.1.
type Bedrock struct{}

func NewBedrock() *Bedrock      { return &Bedrock{} }
func (b *Bedrock) Name() string { return "bedrock" }
func (b *Bedrock) Chat(context.Context, Request) (*Response, error) {
	return nil, ErrNotImplemented
}

// Ollama is a stub for local models. Native implementation is planned post-v0.1.
type Ollama struct{ baseURL string }

func NewOllama(baseURL string) *Ollama { return &Ollama{baseURL: baseURL} }
func (o *Ollama) Name() string         { return "ollama" }
func (o *Ollama) Chat(context.Context, Request) (*Response, error) {
	return nil, ErrNotImplemented
}
