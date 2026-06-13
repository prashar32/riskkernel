package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultOllamaBaseURL is the local Ollama server. Overridable for a remote
// Ollama or a test mock.
const defaultOllamaBaseURL = "http://localhost:11434"

// Ollama implements Provider against a local (or self-hosted) Ollama server's
// /api/chat endpoint — local models, no API key, your machine. Token usage comes
// from Ollama's prompt_eval_count / eval_count so the governor and cost ledger
// meter local runs the same way as hosted ones (a local model is typically priced
// at $0, which the pricing table handles).
type Ollama struct {
	baseURL string
	http    *http.Client
}

// NewOllama constructs an Ollama provider. An empty baseURL uses the local default.
func NewOllama(baseURL string) *Ollama {
	if baseURL == "" {
		baseURL = defaultOllamaBaseURL
	}
	return &Ollama{
		baseURL: strings.TrimRight(baseURL, "/"),
		// Local generation can be slow on first load (model pull/warm); allow more
		// headroom than the hosted providers. The governor's time budget still
		// interrupts via ctx.
		http: &http.Client{Timeout: 300 * time.Second},
	}
}

// WithBaseURL overrides the server URL (empty keeps the current). Returns the
// provider for chaining, matching the other providers.
func (o *Ollama) WithBaseURL(url string) *Ollama {
	if url != "" {
		o.baseURL = strings.TrimRight(url, "/")
	}
	return o
}

// Name returns the stable provider identifier.
func (o *Ollama) Name() string { return "ollama" }

// --- wire types (Ollama /api/chat) ---

type ollamaReq struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  *ollamaOptions  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	NumPredict  int      `json:"num_predict,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type ollamaResp struct {
	Model      string        `json:"model"`
	Message    ollamaMessage `json:"message"`
	DoneReason string        `json:"done_reason"`
	// Ollama reports token counts as eval counts.
	PromptEvalCount int64  `json:"prompt_eval_count"`
	EvalCount       int64  `json:"eval_count"`
	Error           string `json:"error"`
}

// Chat performs one chat completion against Ollama's /api/chat (non-streaming).
func (o *Ollama) Chat(ctx context.Context, req Request) (*Response, error) {
	// Ollama takes the system prompt as a leading system message (like OpenAI).
	msgs := make([]ollamaMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, ollamaMessage{Role: string(RoleSystem), Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, ollamaMessage{Role: string(m.Role), Content: m.Content})
	}

	wire := ollamaReq{Model: req.Model, Messages: msgs, Stream: false}
	if req.MaxTokens > 0 || req.Temperature != nil {
		wire.Options = &ollamaOptions{NumPredict: req.MaxTokens, Temperature: req.Temperature}
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: building request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")

	resp, err := o.http.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("ollama: request failed (is ollama running at %s?): %w", o.baseURL, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ollama: reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr ollamaResp
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("ollama: %s (http %d)", apiErr.Error, resp.StatusCode)
		}
		return nil, fmt.Errorf("ollama: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out ollamaResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ollama: decoding response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama: %s", out.Error)
	}

	return &Response{
		Model:        out.Model,
		Content:      out.Message.Content,
		FinishReason: out.DoneReason,
		Usage: Usage{
			PromptTokens:     out.PromptEvalCount,
			CompletionTokens: out.EvalCount,
		},
	}, nil
}
