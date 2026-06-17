package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultBedrockMaxTokens is used when a Request omits MaxTokens.
const defaultBedrockMaxTokens = 1024

// Bedrock implements Provider against the AWS Bedrock Runtime **Converse** API,
// signed with hand-rolled SigV4 (no AWS SDK dependency — see sigv4.go). The unified
// Converse API works across Bedrock's hosted models and returns token usage, which
// the governor meters and the ledger prices. BYO AWS credentials, read from the
// standard AWS env vars and never stored.
type Bedrock struct {
	creds   awsCreds
	region  string
	baseURL string // override; empty → the regional bedrock-runtime endpoint
	http    *http.Client
}

// NewBedrock constructs a Bedrock provider for the given region and credentials.
func NewBedrock(accessKey, secretKey, sessionToken, region string) *Bedrock {
	return &Bedrock{
		creds:  awsCreds{accessKey: accessKey, secretKey: secretKey, sessionToken: sessionToken},
		region: region,
		http:   &http.Client{Timeout: 120 * time.Second},
	}
}

// WithBaseURL overrides the runtime endpoint — for a VPC/PrivateLink endpoint or a
// test mock. Empty keeps the regional default. Returns the provider for chaining.
func (b *Bedrock) WithBaseURL(u string) *Bedrock {
	if u != "" {
		b.baseURL = strings.TrimRight(u, "/")
	}
	return b
}

// Name returns the stable provider identifier.
func (b *Bedrock) Name() string { return "bedrock" }

func (b *Bedrock) endpoint() string {
	if b.baseURL != "" {
		return b.baseURL
	}
	return "https://bedrock-runtime." + b.region + ".amazonaws.com"
}

// --- Converse wire types ---

type bedrockTextBlock struct {
	Text string `json:"text"`
}

type bedrockMessage struct {
	Role    string             `json:"role"`
	Content []bedrockTextBlock `json:"content"`
}

type bedrockReq struct {
	Messages        []bedrockMessage   `json:"messages"`
	System          []bedrockTextBlock `json:"system,omitempty"`
	InferenceConfig *bedrockInference  `json:"inferenceConfig,omitempty"`
}

type bedrockInference struct {
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type bedrockResp struct {
	Output struct {
		Message bedrockMessage `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	} `json:"usage"`
}

// Chat performs one chat completion against the Bedrock Converse API.
func (b *Bedrock) Chat(ctx context.Context, req Request) (*Response, error) {
	if b.creds.accessKey == "" || b.creds.secretKey == "" {
		return nil, fmt.Errorf("bedrock: missing AWS credentials (set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY)")
	}
	if b.region == "" {
		return nil, fmt.Errorf("bedrock: missing AWS region (set AWS_REGION)")
	}

	// Bedrock takes the system prompt as a separate field. Lift any system message
	// out of Messages and map the rest into Converse content blocks.
	var system []bedrockTextBlock
	if req.System != "" {
		system = append(system, bedrockTextBlock{Text: req.System})
	}
	msgs := make([]bedrockMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			system = append(system, bedrockTextBlock{Text: m.Content})
			continue
		}
		msgs = append(msgs, bedrockMessage{Role: string(m.Role), Content: []bedrockTextBlock{{Text: m.Content}}})
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultBedrockMaxTokens
	}
	body, err := json.Marshal(bedrockReq{
		Messages:        msgs,
		System:          system,
		InferenceConfig: &bedrockInference{MaxTokens: maxTokens, Temperature: req.Temperature},
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock: marshaling request: %w", err)
	}

	// Build the URL so the SigV4 canonical path matches what's sent: the model id
	// can contain ':' (e.g. ...-v1:0), which AWS encodes as %3A — set Path (decoded)
	// and RawPath (AWS-encoded) so EscapedPath() is identical to the wire path.
	u, err := url.Parse(b.endpoint())
	if err != nil {
		return nil, fmt.Errorf("bedrock: bad endpoint %q: %w", b.endpoint(), err)
	}
	u.Path = "/model/" + req.Model + "/converse"
	u.RawPath = "/model/" + awsURIEncodeSegment(req.Model) + "/converse"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bedrock: building request: %w", err)
	}
	httpReq.URL = u
	httpReq.Header.Set("content-type", "application/json")
	signV4(httpReq, body, b.creds, b.region, "bedrock", time.Now())

	resp, err := b.http.Do(httpReq)
	if err != nil {
		// Propagate context errors verbatim so the governor can distinguish a
		// kill-switch/timeout cancellation from a transport failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("bedrock: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Message != "" {
			return nil, fmt.Errorf("bedrock: %s (http %d)", apiErr.Message, resp.StatusCode)
		}
		return nil, fmt.Errorf("bedrock: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out bedrockResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("bedrock: decoding response: %w", err)
	}
	var sb strings.Builder
	for _, c := range out.Output.Message.Content {
		sb.WriteString(c.Text)
	}
	return &Response{
		ID:           resp.Header.Get("x-amzn-RequestId"),
		Model:        req.Model,
		Content:      sb.String(),
		FinishReason: out.StopReason,
		Usage: Usage{
			PromptTokens:     out.Usage.InputTokens,
			CompletionTokens: out.Usage.OutputTokens,
		},
	}, nil
}
