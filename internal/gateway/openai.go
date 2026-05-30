package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/prashar32/riskkernel/internal/httpx"
	"github.com/prashar32/riskkernel/internal/provider"
)

// --- OpenAI chat/completions wire types ---

type oaiChatRequest struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	MaxTokens   int          `json:"max_tokens"`
	Temperature *float64     `json:"temperature"`
	Stream      bool         `json:"stream"`
}

type oaiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type oaiChatResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []oaiChoice `json:"choices"`
	Usage   oaiUsage    `json:"usage"`
}

type oaiChoice struct {
	Index        int       `json:"index"`
	Message      oaiOutMsg `json:"message"`
	FinishReason string    `json:"finish_reason"`
}

type oaiOutMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// handleChatCompletions is the OpenAI-compatible entrypoint — the gold-standard
// zero-code on-ramp (OPENAI_BASE_URL=http://localhost:7070/v1).
func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r, maxBodyBytes)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}
	var req oaiChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if req.Stream {
		httpx.WriteError(w, http.StatusNotImplemented, "streaming_unsupported",
			"streaming is not supported in v0.1 (mid-stream budget enforcement is deferred); set stream:false")
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
	httpx.WriteJSON(w, http.StatusOK, oaiChatResponse{
		ID:      "chatcmpl-" + resp.ID,
		Object:  "chat.completion",
		Created: unixNow(),
		Model:   resp.Model,
		Choices: []oaiChoice{{
			Index:        0,
			Message:      oaiOutMsg{Role: "assistant", Content: resp.Content},
			FinishReason: openAIFinishReason(resp.FinishReason),
		}},
		Usage: oaiUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.Total(),
		},
	})
}

// openAIFinishReason normalizes provider stop reasons to OpenAI's vocabulary so
// existing OpenAI clients behave as expected.
func openAIFinishReason(r string) string {
	switch r {
	case "end_turn", "stop_sequence", "stop":
		return "stop"
	case "max_tokens", "length":
		return "length"
	case "tool_use", "tool_calls":
		return "tool_calls"
	case "":
		return "stop"
	default:
		return r
	}
}
