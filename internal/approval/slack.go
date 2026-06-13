package approval

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prashar32/riskkernel/internal/storage"
)

// slackAPIBase is Slack's Web API root; overridable in tests.
const slackAPIBase = "https://slack.com/api"

// slackMaxSkew bounds how old a signed Slack request may be, to defeat replay.
const slackMaxSkew = 5 * time.Minute

// SlackNotifier is a push channel that posts a pending approval to a Slack channel
// with Approve/Deny buttons, and verifies the signed interaction Slack sends back
// when a human clicks one. Outbound uses a bot token (chat.postMessage); inbound is
// authenticated by the app's signing secret (not the daemon's API token, which
// Slack can't send). The bot token and signing secret are secrets — read from the
// environment, never logged. See SECURITY.md.
type SlackNotifier struct {
	botToken      string
	channel       string
	signingSecret string
	apiBase       string
	client        *http.Client
	log           *slog.Logger
	now           func() time.Time
}

// NewSlackNotifier returns a notifier, or nil if the bot token or channel is unset
// (no Slack push channel). A missing signing secret still allows outbound posts but
// makes the inbound interaction endpoint fail closed (it can't verify Slack).
func NewSlackNotifier(botToken, channel, signingSecret string, log *slog.Logger) *SlackNotifier {
	if botToken == "" || channel == "" {
		return nil
	}
	return &SlackNotifier{
		botToken:      botToken,
		channel:       channel,
		signingSecret: signingSecret,
		apiBase:       slackAPIBase,
		client:        &http.Client{Timeout: 10 * time.Second},
		log:           log,
		now:           time.Now,
	}
}

// Notify posts the approval to Slack asynchronously (best-effort; a flaky Slack
// must never wedge a governed run).
func (s *SlackNotifier) Notify(_ context.Context, a storage.ApprovalRecord) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		body := map[string]any{
			"channel": s.channel,
			"text":    fmt.Sprintf("Approval required: %s on run %s", a.Tool, a.RunID),
			"blocks":  pendingBlocks(a),
		}
		if _, err := s.call(ctx, "chat.postMessage", body); err != nil {
			s.log.Error("slack approval notify failed", "id", a.ID, "err", err)
		}
	}()
}

// VerifySignature checks a Slack request signature (v0 scheme: HMAC-SHA256 of
// "v0:{timestamp}:{body}" keyed by the signing secret), rejecting a stale
// timestamp to defeat replay. Returns false (fail closed) if no signing secret is
// configured. Constant-time compare.
func (s *SlackNotifier) VerifySignature(timestamp, signature string, body []byte) bool {
	if s.signingSecret == "" {
		return false
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return false
	}
	if d := s.now().Unix() - ts; d > int64(slackMaxSkew.Seconds()) || d < -int64(slackMaxSkew.Seconds()) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.signingSecret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(signature))
}

// SlackAction is a parsed Approve/Deny click: which approval, the decision, the
// human who clicked, and the message to update.
type SlackAction struct {
	ApprovalID string
	Approve    bool
	By         string
	Channel    string // for updating the source message
	MessageTS  string
}

// slack action ids on the buttons (and what we match inbound).
const (
	slackApproveAction = "riskkernel_approve"
	slackDenyAction    = "riskkernel_deny"
)

// ParseInteraction decodes a Slack block_actions payload into a SlackAction, or
// (_, false) if it isn't a recognized Approve/Deny click.
func (s *SlackNotifier) ParseInteraction(payload []byte) (SlackAction, bool) {
	var p struct {
		Type string `json:"type"`
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"user"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		Message struct {
			TS string `json:"ts"`
		} `json:"message"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return SlackAction{}, false
	}
	if p.Type != "block_actions" || len(p.Actions) == 0 {
		return SlackAction{}, false
	}
	act := p.Actions[0]
	var approve bool
	switch act.ActionID {
	case slackApproveAction:
		approve = true
	case slackDenyAction:
		approve = false
	default:
		return SlackAction{}, false
	}
	by := p.User.Username
	if by == "" {
		by = p.User.Name
	}
	if by == "" {
		by = p.User.ID
	}
	return SlackAction{
		ApprovalID: act.Value,
		Approve:    approve,
		By:         "slack:" + by,
		Channel:    p.Channel.ID,
		MessageTS:  p.Message.TS,
	}, true
}

// UpdateResolved rewrites the source Slack message to show the decision, replacing
// the buttons so it can't be actioned twice. Best-effort.
func (s *SlackNotifier) UpdateResolved(ctx context.Context, act SlackAction, a storage.ApprovalRecord) {
	if act.Channel == "" || act.MessageTS == "" {
		return
	}
	verb := "🛑 Denied"
	if act.Approve {
		verb = "✅ Approved"
	}
	text := fmt.Sprintf("%s by %s — `%s` on run `%s`", verb, act.By, a.Tool, a.RunID)
	body := map[string]any{
		"channel": act.Channel,
		"ts":      act.MessageTS,
		"text":    text,
		"blocks": []map[string]any{
			{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		},
	}
	if _, err := s.call(ctx, "chat.update", body); err != nil {
		s.log.Warn("slack approval message update failed", "id", a.ID, "err", err)
	}
}

// call POSTs a JSON request to a Slack Web API method and checks the {ok,error}
// envelope Slack returns even on HTTP 200.
func (s *SlackNotifier) call(ctx context.Context, method string, body map[string]any) (map[string]any, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBase+"/"+method, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.botToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("slack %s: decode response: %w", method, err)
	}
	if ok, _ := out["ok"].(bool); !ok {
		return out, fmt.Errorf("slack %s: %v", method, out["error"])
	}
	return out, nil
}

// pendingBlocks renders the Block Kit message for a pending approval.
func pendingBlocks(a storage.ApprovalRecord) []map[string]any {
	header := fmt.Sprintf("*Approval required*\n*Tool:* `%s`", a.Tool)
	if a.SideEffect != "" {
		header += fmt.Sprintf("   *Side effect:* `%s`", a.SideEffect)
	}
	header += fmt.Sprintf("\n*Run:* `%s`   *Step:* %d", a.RunID, a.StepIndex)
	blocks := []map[string]any{
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": header}},
	}
	if len(a.Arguments) > 0 {
		if args, err := json.MarshalIndent(a.Arguments, "", "  "); err == nil {
			blocks = append(blocks, map[string]any{
				"type": "section",
				"text": map[string]any{"type": "mrkdwn", "text": "*Arguments:*\n```" + string(args) + "```"},
			})
		}
	}
	blocks = append(blocks, map[string]any{
		"type":     "actions",
		"block_id": a.ID,
		"elements": []map[string]any{
			{"type": "button", "action_id": slackApproveAction, "style": "primary",
				"text": map[string]any{"type": "plain_text", "text": "Approve"}, "value": a.ID},
			{"type": "button", "action_id": slackDenyAction, "style": "danger",
				"text": map[string]any{"type": "plain_text", "text": "Deny"}, "value": a.ID},
		},
	})
	return blocks
}
