package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
)

const (
	// DefaultMention is the handle that triggers an agent run when AGENT_MENTION
	// is not configured.
	DefaultMention = "@goravel-agent"
	// platformGitHub is the only platform the poller consumes.
	platformGitHub = "github"
	// eventPage is the single page fetched on every tick.
	eventPage = 1
	// eventLimit is the page size fetched on every tick.
	eventLimit = 50
	// The GitHub actions that introduced a mention; other actions repeat the
	// issue/PR body without a new mention.
	eventActionCreated = "created"
	eventActionOpened  = "opened"
)

// envelope is the relay's paginated response wrapper.
type envelope struct {
	Data  []event `json:"data"`
	Total int     `json:"total"`
	Page  int     `json:"page"`
	Limit int     `json:"limit"`
}

// event is a single stored platform event returned by the relay.
type event struct {
	// ID is the relay's numeric primary key; format it with strconv.FormatUint.
	ID         uint64  `json:"id"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	DeletedAt  *string `json:"deleted_at"`
	Platform   string  `json:"platform"`
	DeliveryID string  `json:"delivery_id"`
	Event      string  `json:"event"`
	Action     string  `json:"action"`
	Repository string  `json:"repository"`
	Sender     string  `json:"sender"`
	Payload    string  `json:"payload"`

	// decoded caches the parsed payload so it is decoded at most once per event.
	decoded *githubPayload
	// decodeErr caches the payload decode error alongside decoded.
	decodeErr error
}

// actionable reports whether the event introduced the mention. GitHub repeats
// the issue/PR body on every lifecycle event (labeled, closed, synchronize, …),
// each with its own delivery id, so only the create/open actions are acted on.
func (e *event) actionable() bool {
	switch e.Action {
	case eventActionCreated, eventActionOpened:
		return true
	default:
		return false
	}
}

// instruction reports whether the event mentions the handle and, if so, returns
// the instruction that follows it. A payload decode error is ignored here so the
// poll loop can log it once via payload().
func (e *event) instruction(mention string) (string, bool) {
	payload, _ := e.payload()

	return mentionInstruction(payload, mention)
}

// target returns the issue or pull request number the event refers to, falling
// back from the payload's top-level number to issue.number then
// pull_request.number.
func (e *event) target() int {
	payload, _ := e.payload()
	if payload.Number != 0 {
		return payload.Number
	}
	if payload.Issue.Number != 0 {
		return payload.Issue.Number
	}

	return payload.PullRequest.Number
}

// payload decodes the event's JSON payload, caching both the result and the
// decode error so repeated callers within a single event do not re-parse it.
// The error is returned for the caller to log.
func (e *event) payload() (*githubPayload, error) {
	if e.decoded != nil {
		return e.decoded, e.decodeErr
	}

	payload := &githubPayload{}
	err := json.Unmarshal([]byte(e.Payload), payload)
	e.decoded = payload
	e.decodeErr = err

	return payload, err
}

// githubPayload is the portion of a GitHub event payload the poller cares about.
type githubPayload struct {
	Number  int `json:"number"`
	Comment struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	} `json:"comment"`
	Issue struct {
		Number int    `json:"number"`
		Body   string `json:"body"`
	} `json:"issue"`
	PullRequest struct {
		Number int    `json:"number"`
		Body   string `json:"body"`
	} `json:"pull_request"`
}

// parseEnvelope decodes the relay's {data,total,page,limit} response body.
func parseEnvelope(body []byte) (envelope, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return envelope{}, err
	}

	return env, nil
}

// commentText returns the first non-empty body found in the payload:
// comment.body -> issue.body -> pull_request.body.
func commentText(payload *githubPayload) string {
	if payload.Comment.Body != "" {
		return payload.Comment.Body
	}
	if payload.Issue.Body != "" {
		return payload.Issue.Body
	}

	return payload.PullRequest.Body
}

// mentionInstruction reports whether the text mentions the handle and, if so,
// returns the text that follows it. Matching is case-insensitive and, as with
// GitHub logins, `-` counts as a handle character on both boundaries, so
// `@goravel-agent2`, `@goravel-agent-bot` and `bob@goravel-agent.com` are not
// mentions while `@Goravel-Agent, please review` is. The hyphen inside the
// handle is literal: `@goravel_agent` and `@goravel agent` are not mentions.
// All slicing stays on the original text.
func mentionInstruction(payload *githubPayload, mention string) (string, bool) {
	if mention == "" {
		return "", false
	}

	text := commentText(payload)
	handleLen := len(mention)

	for i := 0; i+handleLen <= len(text); i++ {
		if !strings.EqualFold(text[i:i+handleLen], mention) {
			continue
		}

		if i != 0 && isHandleByte(text[i-1]) {
			continue
		}

		end := i + handleLen
		if end != len(text) && isHandleByte(text[end]) {
			continue
		}

		instruction := strings.Trim(text[end:], " \t\r\n,.;:!?*_`~#()[]{}\"'")
		if !hasLetterOrDigit(instruction) {
			return "", false
		}

		return instruction, true
	}

	return "", false
}

// hasLetterOrDigit reports whether s contains at least one letter or digit.
func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}

	return false
}

// isHandleByte reports whether b continues a GitHub login: a letter, digit,
// underscore or hyphen.
func isHandleByte(b byte) bool {
	return isWordByte(b) || b == '-'
}

// isWordByte reports whether b is an ASCII letter, digit or underscore.
func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}

// prompt builds the instruction passed to the agent, including the trusted
// context the agent needs to act on the event. The fixed context lines are
// emitted before the untrusted instruction so a commenter cannot spoof them.
// This is the event-to-agent contract: the poller decides when to use it and
// the dispatcher decides how to run it.
func (e *event) prompt(instruction string) string {
	payload, _ := e.payload()

	var builder strings.Builder
	builder.WriteString("Repository: ")
	builder.WriteString(e.Repository)
	builder.WriteString("\n")
	builder.WriteString("Event: ")
	builder.WriteString(e.Event)
	builder.WriteString("\n")
	if payload.Comment.ID != 0 {
		builder.WriteString("Comment ID: ")
		builder.WriteString(strconv.FormatInt(payload.Comment.ID, 10))
		builder.WriteString("\n")
	}
	builder.WriteString("Number: #")
	builder.WriteString(strconv.Itoa(e.target()))
	builder.WriteString("\n\nInstruction:\n")
	builder.WriteString(instruction)

	return builder.String()
}
