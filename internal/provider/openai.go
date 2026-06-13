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

// defaultOpenAIBaseURL is the OpenAI API base. Overridable for tests and for
// OpenAI-compatible gateways the user may front.
const defaultOpenAIBaseURL = "https://api.openai.com"

// OpenAI implements Provider against the OpenAI Chat Completions API.
type OpenAI struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewOpenAI constructs an OpenAI provider.
func NewOpenAI(apiKey string) *OpenAI {
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: defaultOpenAIBaseURL,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// WithBaseURL overrides the API base — point it at an OpenAI-compatible gateway,
// a corporate proxy, or a local mock (e.g. for benchmarking). Empty keeps the
// default. Returns the provider for chaining.
func (o *OpenAI) WithBaseURL(url string) *OpenAI {
	if url != "" {
		o.baseURL = strings.TrimRight(url, "/")
	}
	return o
}

// Name returns the stable provider identifier.
func (o *OpenAI) Name() string { return "openai" }

// --- wire types (OpenAI Chat Completions API) ---

type openAIReq struct {
	Model         string            `json:"model"`
	Messages      []openAIMessage   `json:"messages"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	StreamOptions *oaiStreamOptions `json:"stream_options,omitempty"`
}

// oaiStreamOptions asks OpenAI to include a final usage chunk in the stream, so
// the gateway can meter a streamed call.
type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResp struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

type openAIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Chat performs one chat completion against the OpenAI Chat Completions API.
func (o *OpenAI) Chat(ctx context.Context, req Request) (*Response, error) {
	if o.apiKey == "" {
		return nil, fmt.Errorf("openai: missing API key")
	}

	// OpenAI takes the system prompt as a leading system message.
	msgs := make([]openAIMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openAIMessage{Role: string(RoleSystem), Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, openAIMessage{Role: string(m.Role), Content: m.Content})
	}

	body, err := json.Marshal(openAIReq{
		Model:       req.Model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens, // omitted when zero; OpenAI doesn't require it
		Temperature: req.Temperature,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: building request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+o.apiKey)

	resp, err := o.http.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr openAIError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("openai: %s (%s, http %d)", apiErr.Error.Message, apiErr.Error.Type, resp.StatusCode)
		}
		return nil, fmt.Errorf("openai: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out openAIResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("openai: decoding response: %w", err)
	}

	var content, finish string
	if len(out.Choices) > 0 {
		content = out.Choices[0].Message.Content
		finish = out.Choices[0].FinishReason
	}

	return &Response{
		ID:           out.ID,
		Model:        out.Model,
		Content:      content,
		FinishReason: finish,
		Usage: Usage{
			PromptTokens:     out.Usage.PromptTokens,
			CompletionTokens: out.Usage.CompletionTokens,
		},
	}, nil
}

// ChatStream implements the Streamer interface: a streaming chat completion that
// yields OpenAI's raw SSE chunks verbatim while accumulating usage. It asks for a
// final usage chunk (stream_options.include_usage) so the call can be metered.
func (o *OpenAI) ChatStream(ctx context.Context, req Request) (ChatStream, error) {
	if o.apiKey == "" {
		return nil, fmt.Errorf("openai: missing API key")
	}

	msgs := make([]openAIMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openAIMessage{Role: string(RoleSystem), Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, openAIMessage{Role: string(m.Role), Content: m.Content})
	}

	body, err := json.Marshal(openAIReq{
		Model: req.Model, Messages: msgs, MaxTokens: req.MaxTokens, Temperature: req.Temperature,
		Stream: true, StreamOptions: &oaiStreamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshaling stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: building stream request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+o.apiKey)
	httpReq.Header.Set("accept", "text/event-stream")

	resp, err := o.http.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("openai: stream request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		var apiErr openAIError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("openai: %s (%s, http %d)", apiErr.Error.Message, apiErr.Error.Type, resp.StatusCode)
		}
		return nil, fmt.Errorf("openai: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return &oaiStream{body: resp.Body, r: bufio.NewReader(resp.Body)}, nil
}

// oaiStream forwards OpenAI's SSE bytes line-by-line (verbatim, so the client sees
// authentic SSE) while sniffing each `data:` line for the model and the final
// usage chunk.
type oaiStream struct {
	body  io.ReadCloser
	r     *bufio.Reader
	usage Usage
	model string
}

var sseData = []byte("data:")

// Recv returns the next raw SSE line (including its trailing newline) to forward,
// or io.EOF at the end. Usage/model are updated from data lines as they pass.
func (s *oaiStream) Recv() ([]byte, error) {
	line, err := s.r.ReadBytes('\n')
	if len(line) > 0 {
		s.sniff(line)
	}
	return line, err
}

// sniff parses a `data: {json}` line for the model and a usage block, ignoring the
// terminal `data: [DONE]` and non-data lines (event:, blank, comments).
func (s *oaiStream) sniff(line []byte) {
	t := bytes.TrimSpace(line)
	if !bytes.HasPrefix(t, sseData) {
		return
	}
	payload := bytes.TrimSpace(t[len(sseData):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var chunk struct {
		Model string    `json:"model"`
		Usage *oaiUsage `json:"usage"`
	}
	if json.Unmarshal(payload, &chunk) != nil {
		return
	}
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.Usage != nil {
		s.usage = Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens}
	}
}

func (s *oaiStream) Usage() Usage  { return s.usage }
func (s *oaiStream) Model() string { return s.model }
func (s *oaiStream) Close() error  { return s.body.Close() }

// oaiUsage is the OpenAI usage block (also used by the streaming sniffer).
type oaiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}
