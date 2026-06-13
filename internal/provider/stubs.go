package provider

import "context"

// The provider below is a stub. It satisfies the Provider interface so routing and
// config can reference it, but Chat returns ErrNotImplemented until it is built
// out. For the long tail of providers, the documented path is to front RiskKernel
// with LiteLLM rather than reimplement 100+ vendors here. (Anthropic, OpenAI, and
// Ollama are implemented natively — see anthropic.go, openai.go, ollama.go.)

// Bedrock is a stub. Native implementation is planned.
type Bedrock struct{}

func NewBedrock() *Bedrock      { return &Bedrock{} }
func (b *Bedrock) Name() string { return "bedrock" }
func (b *Bedrock) Chat(context.Context, Request) (*Response, error) {
	return nil, ErrNotImplemented
}
