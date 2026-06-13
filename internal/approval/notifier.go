package approval

import (
	"context"

	"github.com/prashar32/riskkernel/internal/storage"
)

// multiNotifier fans a pending approval out to several push channels (e.g. a
// webhook and Slack at once).
type multiNotifier []Notifier

func (m multiNotifier) Notify(ctx context.Context, a storage.ApprovalRecord) {
	for _, n := range m {
		n.Notify(ctx, a)
	}
}

// CombineNotifiers returns a single Notifier over the given channels, dropping
// nil ones. Returns nil when none remain (the Gate treats nil as "no push
// channel"), or the sole notifier unwrapped when only one is configured.
func CombineNotifiers(notifiers ...Notifier) Notifier {
	live := make(multiNotifier, 0, len(notifiers))
	for _, n := range notifiers {
		if n != nil && !isNilNotifier(n) {
			live = append(live, n)
		}
	}
	switch len(live) {
	case 0:
		return nil
	case 1:
		return live[0]
	default:
		return live
	}
}

// isNilNotifier reports whether a Notifier is a typed-nil concrete pointer (e.g. a
// (*SlackNotifier)(nil) returned when Slack isn't configured), which is non-nil as
// an interface but must be treated as absent.
func isNilNotifier(n Notifier) bool {
	switch v := n.(type) {
	case *SlackNotifier:
		return v == nil
	case *WebhookNotifier:
		return v == nil
	default:
		return false
	}
}
