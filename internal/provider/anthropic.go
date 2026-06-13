package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// anthropicAPIVersion pins the Anthropic API version header. Bumping it is a
// deliberate, documented change.
const anthropicAPIVersion = "2023-06-01"

// defaultAnthropicBaseURL is the Anthropic Messages API base. Overridable for
// tests and for users who front Anthropic with their own proxy.
const defaultAnthropicBaseURL = "https://api.anthropic.com"

// defaultMaxTokens is used when a Request omits MaxTokens (Anthropic requires it).
const defaultMaxTokens = 1024

// Anthropic implements Provider against the Anthropic Messages API. It is the
// native v0.1 provider (the founder builds on Claude).
type Anthropic struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewAnthropic constructs an Anthropic provider. apiKey must be non-empty.
func NewAnthropic(apiKey string) *Anthropic {
	return &Anthropic{
		apiKey:  apiKey,
		baseURL: defaultAnthropicBaseURL,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// WithBaseURL overrides the API base — point it at a proxy that fronts Anthropic
// or a local mock (e.g. for benchmarking). Empty keeps the default. Returns the
// provider for chaining.
func (a *Anthropic) WithBaseURL(url string) *Anthropic {
	if url != "" {
		a.baseURL = strings.TrimRight(url, "/")
	}
	return a
}

// Name returns the stable provider identifier.
func (a *Anthropic) Name() string { return "anthropic" }

// --- wire types (Anthropic Messages API) ---

type anthropicReq struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResp struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Chat performs one chat completion against the Anthropic Messages API.
func (a *Anthropic) Chat(ctx context.Context, req Request) (*Response, error) {
	if a.apiKey == "" {
		return nil, fmt.Errorf("anthropic: missing API key")
	}
	system, msgs := splitSystem(req)
	body, err := json.Marshal(anthropicReq{
		Model:       req.Model,
		MaxTokens:   anthropicMaxTokens(req),
		System:      system,
		Messages:    msgs,
		Temperature: req.Temperature,
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: building request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := a.http.Do(httpReq)
	if err != nil {
		// Propagate context errors verbatim so the governor can distinguish a
		// kill-switch/timeout cancellation from a transport failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr anthropicError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("anthropic: %s (%s, http %d)", apiErr.Error.Message, apiErr.Error.Type, resp.StatusCode)
		}
		return nil, fmt.Errorf("anthropic: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out anthropicResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("anthropic: decoding response: %w", err)
	}

	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}

	return &Response{
		ID:           out.ID,
		Model:        out.Model,
		Content:      sb.String(),
		FinishReason: out.StopReason,
		Usage: Usage{
			PromptTokens:     out.Usage.InputTokens,
			CompletionTokens: out.Usage.OutputTokens,
		},
	}, nil
}

// anthropicMaxTokens returns the request's MaxTokens, falling back to the default
// (Anthropic requires the field to be present and positive).
func anthropicMaxTokens(req Request) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return defaultMaxTokens
}

// splitSystem lifts any system message out of req.Messages and merges it with
// req.System — Anthropic takes the system prompt as a top-level field — returning
// the system prompt and the remaining conversation messages.
func splitSystem(req Request) (string, []anthropicMessage) {
	system := req.System
	msgs := make([]anthropicMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			if system == "" {
				system = m.Content
			} else {
				system = system + "\n\n" + m.Content
			}
			continue
		}
		msgs = append(msgs, anthropicMessage{Role: string(m.Role), Content: m.Content})
	}
	return system, msgs
}

// ChatStream implements the Streamer interface: a streaming completion that yields
// Anthropic's raw SSE events verbatim (so the client receives authentic Anthropic
// SSE) while accumulating token usage for metering. Usage is assembled from the
// stream's own accounting: message_start carries input_tokens (and the model),
// message_delta carries the final cumulative output_tokens.
func (a *Anthropic) ChatStream(ctx context.Context, req Request) (ChatStream, error) {
	if a.apiKey == "" {
		return nil, fmt.Errorf("anthropic: missing API key")
	}

	system, msgs := splitSystem(req)
	body, err := json.Marshal(anthropicReq{
		Model:       req.Model,
		MaxTokens:   anthropicMaxTokens(req),
		System:      system,
		Messages:    msgs,
		Temperature: req.Temperature,
		Stream:      true,
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshaling stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: building stream request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
	httpReq.Header.Set("accept", "text/event-stream")

	resp, err := a.http.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("anthropic: stream request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		var apiErr anthropicError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("anthropic: %s (%s, http %d)", apiErr.Error.Message, apiErr.Error.Type, resp.StatusCode)
		}
		return nil, fmt.Errorf("anthropic: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return &antStream{body: resp.Body, r: bufio.NewReader(resp.Body)}, nil
}

// antStream forwards Anthropic's SSE bytes line-by-line (verbatim, so the client
// sees authentic Anthropic events) while sniffing the data lines for the model and
// token usage.
type antStream struct {
	body  io.ReadCloser
	r     *bufio.Reader
	usage Usage
	model string
}

// Recv returns the next raw SSE line (including its trailing newline) to forward,
// or io.EOF at the end. Usage/model are updated from data lines as they pass.
func (s *antStream) Recv() ([]byte, error) {
	line, err := s.r.ReadBytes('\n')
	if len(line) > 0 {
		s.sniff(line)
	}
	return line, err
}

// sniff parses a `data: {json}` line for the model (message_start) and token usage
// (input_tokens on message_start, final output_tokens on message_delta), ignoring
// `event:` lines, blanks, and content deltas.
func (s *antStream) sniff(line []byte) {
	t := bytes.TrimSpace(line)
	if !bytes.HasPrefix(t, sseData) {
		return
	}
	payload := bytes.TrimSpace(t[len(sseData):])
	if len(payload) == 0 {
		return
	}
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Model string          `json:"model"`
			Usage *antStreamUsage `json:"usage"`
		} `json:"message"`
		Usage *antStreamUsage `json:"usage"`
	}
	if json.Unmarshal(payload, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		if ev.Message == nil {
			return
		}
		if ev.Message.Model != "" {
			s.model = ev.Message.Model
		}
		if u := ev.Message.Usage; u != nil {
			s.usage.PromptTokens = u.InputTokens
			s.usage.CompletionTokens = u.OutputTokens
		}
	case "message_delta":
		// message_delta carries the running (final, at stream end) output token
		// count, and on cache paths an updated input count.
		if u := ev.Usage; u != nil {
			if u.OutputTokens > 0 {
				s.usage.CompletionTokens = u.OutputTokens
			}
			if u.InputTokens > 0 {
				s.usage.PromptTokens = u.InputTokens
			}
		}
	}
}

func (s *antStream) Usage() Usage  { return s.usage }
func (s *antStream) Model() string { return s.model }
func (s *antStream) Close() error  { return s.body.Close() }

// antStreamUsage is the usage block carried on Anthropic stream events.
type antStreamUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}
