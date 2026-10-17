package agent

import (
	"strconv"
	"strings"
)

// parseSenders splits a comma-separated allowlist into lowercased sender keys,
// ignoring blank entries.
func parseSenders(raw string) map[string]struct{} {
	senders := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			senders[part] = struct{}{}
		}
	}

	return senders
}

// normalizeMention turns the configured handle into a mention: it trims
// surrounding space, ensures the leading `@` and falls back to DefaultMention
// when the value is blank.
func normalizeMention(raw string) string {
	mention := strings.TrimSpace(raw)
	if mention == "" {
		return DefaultMention
	}
	if !strings.HasPrefix(mention, "@") {
		mention = "@" + mention
	}

	return mention
}

// retryKey identifies an event for retry accounting, preferring the relay's
// delivery id and falling back to the event id when it is absent.
func retryKey(ev *event) string {
	if ev.DeliveryID != "" {
		return ev.DeliveryID
	}

	return strconv.FormatUint(ev.ID, 10)
}
