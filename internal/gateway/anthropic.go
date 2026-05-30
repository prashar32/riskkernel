package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/prashar32/riskkernel/internal/httpx"
	"github.com/prashar32/riskkernel/internal/provider"
)

// --- Anthropic Messages wire types ---

type antMessagesRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	System      json.RawMessage `json:"system"`
	Messages    []antMessage    `json:"messages"`
	Temperature *float64        `json:"temperature"`
	Stream      bool            `json:"stream"`
}

type antMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type antMessagesResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	Content    []antTextBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      antUsage       `json:"usage"`
}

type antTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type antUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// handleMessages is the Anthropic-compatible entrypoint
// (ANTHROPIC_BASE_URL=http://localhost:7070). Maps cleanly to the native
// Anthropic provider, governed by the same deterministic core.
func (g *Gateway) handleMessages(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r, maxBodyBytes)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}
	var req antMessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Stream {
		httpx.WriteError(w, http.StatusNotImplemented, "streaming_unsupported",
			"streaming is not supported in v0.1; set stream:false")
		return
	}
	if req.Model == "" || len(req.Messages) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "model and messages are required")
		return
	}

	preq := provider.Request{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		System:      decodeContent(req.System),
		Messages:    make([]provider.Message, 0, len(req.Messages)),
	}
	for _, m := range req.Messages {
		preq.Messages = append(preq.Messages, provider.Message{
			Role:    provider.Role(m.Role),
			Content: decodeContent(m.Content),
		})
	}

	run := g.resolveRun(r)
	resp, meta, gwErr := g.governedCall(r, run, preq)
	if gwErr != nil {
		gwErr.write(w)
		return
	}

	stampHeaders(w, run, resp, meta)
	httpx.WriteJSON(w, http.StatusOK, antMessagesResponse{
		ID:         resp.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      resp.Model,
		Content:    []antTextBlock{{Type: "text", Text: resp.Content}},
		StopReason: defaultStr(resp.FinishReason, "end_turn"),
		Usage: antUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	})
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
