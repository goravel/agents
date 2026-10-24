package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"goravel/app/constants"
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
	// eventPullRequestReviewComment is the GitHub event for an inline review
	// comment; its comment is addressed through the pull request collection.
	eventPullRequestReviewComment = "pull_request_review_comment"
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

// defaultTitle names the run after the issue or pull request the mention came
// from, used when the router returns no title. A pull request yields `PR #42`,
// an issue `Issue #123`. It returns "" when the payload carries no number, so
// the dispatcher omits --title and paseo names the run; a payload that fails to
// decode also degrades to "".
func (e *event) defaultTitle() string {
	number := e.target()
	if number == 0 {
		return ""
	}

	label := issueLabel
	if payload, _ := e.payload(); isPullRequest(payload) {
		label = prLabel
	}

	return label + " #" + strconv.Itoa(number)
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
		ID      int64  `json:"id"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
	} `json:"comment"`
	Issue struct {
		Number      int    `json:"number"`
		Body        string `json:"body"`
		HTMLURL     string `json:"html_url"`
		PullRequest *struct {
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
	} `json:"issue"`
	PullRequest struct {
		Number  int    `json:"number"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
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

// Prompt line labels. They are the fixed context lines shared by the event
// prompt and the router's system prompt, so the router's vocabulary cannot
// drift from the prompt it steers.
const (
	repositoryLabel  = "Repository"
	prLabel          = "PR"
	issueLabel       = "Issue"
	commentLabel     = "comment"
	instructionLabel = "Instruction"
)

// reviewInstructionSuffix is the trusted line appended under the Instruction for
// the review agent when the event targets a pull request, steering the run to
// post its findings there. It is worded as the user's explicit request because
// the review agent only posts a PR comment when the user asks for one.
const reviewInstructionSuffix = "comment the review result to the pr"

// prompt builds the context block the router sees: the source repository line,
// the PR/Issue/comment links, and the untrusted instruction. The fixed lines are
// emitted before the untrusted instruction so its text is read as the
// instruction, not as one of the trusted lines. The instruction may still name
// the repository to work on, which the router honours (see
// routerAgent.Instructions): routing runs only for allowlisted senders
// (AGENT_ALLOWED_SENDERS), which is where the comment text is trusted.
func (e *event) prompt(instruction, repository string) string {
	return repositoryLabel + ": " + repository + "\n" + e.contextBlock(instruction)
}

// agentPrompt builds the context block handed to the agent. It carries the same
// links and instruction as prompt but omits the Repository line: the agent runs
// inside the checkout, so the repository is implied by its working directory.
// The review agent is additionally steered to post its result on the pull
// request; every other agent's prompt is unchanged.
func (e *event) agentPrompt(agentName, instruction string) string {
	payload, _ := e.payload()

	prompt := e.contextBlock(instruction)
	if agentName == constants.AgentReview && isPullRequest(payload) {
		prompt += "\n" + reviewInstructionSuffix
	}

	return prompt
}

// contextBlock emits the PR/Issue/comment links followed by the instruction.
func (e *event) contextBlock(instruction string) string {
	payload, _ := e.payload()

	var builder strings.Builder
	if link := e.pullRequestLink(payload); link != "" {
		builder.WriteString(prLabel + ": ")
		builder.WriteString(link)
		builder.WriteString("\n")
	}
	if link := e.issueLink(payload); link != "" {
		builder.WriteString(issueLabel + ": ")
		builder.WriteString(link)
		builder.WriteString("\n")
	}
	if link := e.commentLink(payload); link != "" {
		builder.WriteString(commentLabel + ": ")
		builder.WriteString(link)
		builder.WriteString("\n")
	}

	builder.WriteString(instructionLabel + ": ")
	builder.WriteString(instruction)

	return builder.String()
}

// isPullRequest reports whether the event targets a pull request.
func isPullRequest(payload *githubPayload) bool {
	return payload.PullRequest.HTMLURL != "" ||
		payload.PullRequest.Number != 0 ||
		payload.Issue.PullRequest != nil
}

// pullRequestLink returns the pull request URL, or "" when the event does not
// target a pull request.
func (e *event) pullRequestLink(payload *githubPayload) string {
	switch {
	case payload.PullRequest.HTMLURL != "":
		return payload.PullRequest.HTMLURL
	case payload.Issue.PullRequest != nil && payload.Issue.HTMLURL != "":
		return payload.Issue.HTMLURL
	case isPullRequest(payload) && e.target() != 0:
		return gitWebURL + e.Repository + "/pull/" + strconv.Itoa(e.target())
	default:
		return ""
	}
}

// issueLink returns the issue URL, or "" when the event targets a pull request.
func (e *event) issueLink(payload *githubPayload) string {
	if isPullRequest(payload) {
		return ""
	}
	if payload.Issue.HTMLURL != "" {
		return payload.Issue.HTMLURL
	}
	if e.target() != 0 {
		return gitWebURL + e.Repository + "/issues/" + strconv.Itoa(e.target())
	}

	return ""
}

// commentLink returns the triggering comment URL, or "" when there is none.
func (e *event) commentLink(payload *githubPayload) string {
	if payload.Comment.HTMLURL != "" {
		return payload.Comment.HTMLURL
	}
	if payload.Comment.ID == 0 {
		return ""
	}

	path := "issues"
	if isPullRequest(payload) {
		path = "pull"
	}

	return gitWebURL + e.Repository + "/" + path + "/" +
		strconv.Itoa(e.target()) + "#issuecomment-" + strconv.FormatInt(payload.Comment.ID, 10)
}

// reaction reports which comment the eyes reaction belongs to. A comment
// mention carries the comment under `comment`; an issue or pull request body
// mention has no comment to react to, so ok is false. Inline review comments
// live under the pull request collection, conversation comments under issues.
func (e *event) reaction() (collection string, commentID int64, ok bool) {
	payload, _ := e.payload()
	if payload.Comment.ID == 0 {
		return "", 0, false
	}

	collection = reactionCollectionIssue
	if e.Event == eventPullRequestReviewComment {
		collection = reactionCollectionReview
	}

	return collection, payload.Comment.ID, true
}
