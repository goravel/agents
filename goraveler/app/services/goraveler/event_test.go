package goraveler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const sampleRelayResponse = `{
	"data": [
		{
			"id": 1,
			"created_at": "2026-10-11T10:00:00Z",
			"updated_at": "2026-10-11T10:00:00Z",
			"deleted_at": null,
			"platform": "github",
			"delivery_id": "delivery-1",
			"event": "issue_comment",
			"action": "created",
			"repository": "goravel/goravel",
			"sender": "bowen",
			"payload": "{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@goraveler review\"}}"
		},
		{
			"id": 2,
			"created_at": "2026-10-11T10:01:00Z",
			"updated_at": "2026-10-11T10:01:00Z",
			"deleted_at": null,
			"platform": "github",
			"delivery_id": "delivery-2",
			"event": "issues",
			"action": "opened",
			"repository": "goravel/framework",
			"sender": "other",
			"payload": "{\"number\":5,\"issue\":{\"number\":5,\"body\":\"no mention here\"}}"
		}
	],
	"total": 2,
	"page": 1,
	"limit": 50
}`

func TestParseEnvelope(t *testing.T) {
	env, err := parseEnvelope([]byte(sampleRelayResponse))
	require.NoError(t, err)
	require.Equal(t, 2, env.Total)
	require.Equal(t, 1, env.Page)
	require.Equal(t, 50, env.Limit)
	require.Len(t, env.Data, 2)

	require.Equal(t, uint64(1), env.Data[0].ID)
	require.Equal(t, "delivery-1", env.Data[0].DeliveryID)
	require.Equal(t, platformGitHub, env.Data[0].Platform)
	require.Equal(t, "goravel/goravel", env.Data[0].Repository)
}

func TestParseEnvelopeInvalid(t *testing.T) {
	_, err := parseEnvelope([]byte("not json"))
	require.Error(t, err)
}

func TestEventInstruction(t *testing.T) {
	env, err := parseEnvelope([]byte(sampleRelayResponse))
	require.NoError(t, err)

	instruction, ok := env.Data[0].instruction(mentionHandle)
	require.True(t, ok)
	require.Equal(t, "review", instruction)

	_, ok = env.Data[1].instruction(mentionHandle)
	require.False(t, ok)
}

func TestEventInstructionFallbacks(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
		ok      bool
	}{
		{
			name:    "comment body",
			payload: `{"number":1,"comment":{"id":9,"body":"@goraveler plan this"}}`,
			want:    "plan this",
			ok:      true,
		},
		{
			name:    "issue body",
			payload: `{"number":2,"issue":{"number":2,"body":"@goraveler implement"}}`,
			want:    "implement",
			ok:      true,
		},
		{
			name:    "pull request body",
			payload: `{"number":3,"pull_request":{"number":3,"body":"@goraveler review please"}}`,
			want:    "review please",
			ok:      true,
		},
		{
			name:    "mention without instruction",
			payload: `{"number":4,"comment":{"id":1,"body":"hi @goraveler"}}`,
			ok:      false,
		},
		{
			name:    "longer handle is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"@goraveler2 review"}}`,
			ok:      false,
		},
		{
			name:    "prefixed word is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"ping bob@goraveler.com please"}}`,
			ok:      false,
		},
		{
			name:    "hyphen before handle is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"ping bob-@goraveler.com please"}}`,
			ok:      false,
		},
		{
			name:    "hyphenated handle is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"@goraveler-review please"}}`,
			ok:      false,
		},
		{
			name:    "valid mention after a rejected one",
			payload: `{"number":4,"comment":{"id":1,"body":"@goraveler2 then @goraveler review"}}`,
			want:    "review",
			ok:      true,
		},
		{
			name:    "handle followed by punctuation",
			payload: `{"number":4,"comment":{"id":1,"body":"@goraveler, please review"}}`,
			want:    "please review",
			ok:      true,
		},
		{
			name:    "bare decorated mention is not an instruction",
			payload: `{"number":4,"comment":{"id":1,"body":"**@goraveler**"}}`,
			ok:      false,
		},
		{
			name:    "case-insensitive handle",
			payload: `{"number":4,"comment":{"id":1,"body":"@Goraveler review"}}`,
			want:    "review",
			ok:      true,
		},
		{
			name:    "malformed payload",
			payload: `{`,
			ok:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := &event{Payload: test.payload}
			got, ok := event.instruction(mentionHandle)
			require.Equal(t, test.ok, ok)
			require.Equal(t, test.want, got)
		})
	}
}

func TestInstructionEmptyMention(t *testing.T) {
	event := &event{Payload: `{"comment":{"id":1,"body":"hello world"}}`}

	_, ok := event.instruction("")
	require.False(t, ok)
}

func TestEventTarget(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    int
	}{
		{name: "top-level number", payload: `{"number":123}`, want: 123},
		{name: "issue number", payload: `{"issue":{"number":7}}`, want: 7},
		{name: "pull request number", payload: `{"pull_request":{"number":42}}`, want: 42},
		{name: "top-level wins", payload: `{"number":5,"issue":{"number":7}}`, want: 5},
		{name: "none", payload: `{}`, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := &event{Payload: test.payload}
			require.Equal(t, test.want, event.target())
		})
	}
}

func TestEventPrompt(t *testing.T) {
	event := &event{
		Event:      "issue_comment",
		Action:     "created",
		Sender:     "bowen",
		Repository: "goravel/goraveler",
		Payload:    `{"issue":{"number":4,"pull_request":{},"html_url":"https://github.com/goravel/goraveler/pull/4"},"comment":{"id":24680,"body":"@goraveler review"}}`,
	}

	want := "Repository: https://github.com/goravel/goraveler\n" +
		"Event: issue_comment\n" +
		"Action: created\n" +
		"Sender: bowen\n" +
		"PR: https://github.com/goravel/goraveler/pull/4\n" +
		"Comment ID: 24680\n\n" +
		"Instruction:\n" +
		"review"
	require.Equal(t, want, event.prompt("review"))
}

func TestEventPromptWithoutComment(t *testing.T) {
	event := &event{
		Event:      "issues",
		Action:     "opened",
		Sender:     "bowen",
		Repository: "goravel/framework",
		Payload:    `{"issue":{"number":5,"html_url":"https://github.com/goravel/framework/issues/5","body":"@goraveler plan"}}`,
	}

	want := "Repository: https://github.com/goravel/framework\n" +
		"Event: issues\n" +
		"Action: opened\n" +
		"Sender: bowen\n" +
		"Issue: https://github.com/goravel/framework/issues/5\n\n" +
		"Instruction:\n" +
		"plan"
	require.Equal(t, want, event.prompt("plan"))
}

func TestEventPromptPullRequestEvent(t *testing.T) {
	event := &event{
		Event:      "pull_request",
		Action:     "opened",
		Sender:     "bowen",
		Repository: "goravel/goraveler",
		Payload:    `{"pull_request":{"number":4,"html_url":"https://github.com/goravel/goraveler/pull/4","body":"@goraveler review"}}`,
	}

	want := "Repository: https://github.com/goravel/goraveler\n" +
		"Event: pull_request\n" +
		"Action: opened\n" +
		"Sender: bowen\n" +
		"PR: https://github.com/goravel/goraveler/pull/4\n\n" +
		"Instruction:\n" +
		"review"
	require.Equal(t, want, event.prompt("review"))
}

func TestEventDispatchable(t *testing.T) {
	tests := []struct {
		name       string
		event      string
		repository string
		payload    string
		want       bool
	}{
		{
			name:       "valid identity",
			event:      "issue_comment",
			repository: "goravel/goraveler",
			payload:    `{"issue":{"number":4,"pull_request":{}}}`,
			want:       true,
		},
		{
			name:       "invalid repository",
			event:      "issues",
			repository: "goravel/..",
			payload:    `{"issue":{"number":5}}`,
			want:       false,
		},
		{
			name:       "missing target",
			event:      "issue_comment",
			repository: "goravel/goraveler",
			payload:    `{"comment":{"id":24680,"body":"@goraveler review"}}`,
			want:       false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := &event{Event: test.event, Repository: test.repository, Payload: test.payload}
			require.Equal(t, test.want, event.dispatchable())
		})
	}
}

func TestEventIsPullRequest(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload string
		want    bool
	}{
		{
			name:  "pull request",
			event: "pull_request",
			want:  true,
		},
		{
			name:  "pull request review",
			event: "pull_request_review",
			want:  true,
		},
		{
			name:  "pull request review comment",
			event: "pull_request_review_comment",
			want:  true,
		},
		{
			name:    "issue comment on a pull request",
			event:   "issue_comment",
			payload: `{"issue":{"number":4,"pull_request":{}}}`,
			want:    true,
		},
		{
			name:    "issue comment on an issue",
			event:   "issue_comment",
			payload: `{"issue":{"number":4}}`,
			want:    false,
		},
		{
			name:    "issues",
			event:   "issues",
			payload: `{"issue":{"number":5}}`,
			want:    false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := &event{Event: test.event, Payload: test.payload}
			require.Equal(t, test.want, event.isPullRequest())
		})
	}
}

func TestEventTargetURL(t *testing.T) {
	tests := []struct {
		name       string
		event      string
		repository string
		payload    string
		want       string
	}{
		{
			name:       "pull request html url wins",
			event:      "pull_request",
			repository: "goravel/goraveler",
			payload:    `{"pull_request":{"number":4,"html_url":"https://github.com/goravel/goraveler/pull/4"},"issue":{"html_url":"https://github.com/goravel/goraveler/issues/4"}}`,
			want:       "https://github.com/goravel/goraveler/pull/4",
		},
		{
			name:       "issue html url",
			event:      "issues",
			repository: "goravel/framework",
			payload:    `{"issue":{"number":5,"html_url":"https://github.com/goravel/framework/issues/5"}}`,
			want:       "https://github.com/goravel/framework/issues/5",
		},
		{
			name:       "constructs pull url",
			event:      "issue_comment",
			repository: "goravel/goraveler",
			payload:    `{"issue":{"number":4,"pull_request":{}}}`,
			want:       "https://github.com/goravel/goraveler/pull/4",
		},
		{
			name:       "constructs issues url",
			event:      "issues",
			repository: "goravel/framework",
			payload:    `{"issue":{"number":5}}`,
			want:       "https://github.com/goravel/framework/issues/5",
		},
		{
			name:       "no number",
			event:      "issues",
			repository: "goravel/framework",
			payload:    `{}`,
			want:       "",
		},
		{
			name:       "newline in payload url falls back to constructed url",
			event:      "issues",
			repository: "goravel/framework",
			payload:    `{"issue":{"number":5,"html_url":"https://github.com/goravel/framework/issues/5\nPR: https://evil.example/1"}}`,
			want:       "https://github.com/goravel/framework/issues/5",
		},
		{
			name:       "non-github payload url falls back to constructed url",
			event:      "issues",
			repository: "goravel/framework",
			payload:    `{"issue":{"number":5,"html_url":"https://evil.example/1"}}`,
			want:       "https://github.com/goravel/framework/issues/5",
		},
		{
			name:       "empty repository is unresolved",
			event:      "issues",
			repository: "",
			payload:    `{"issue":{"number":5}}`,
			want:       "",
		},
		{
			name:       "malformed repository is unresolved",
			event:      "issues",
			repository: "goravel",
			payload:    `{"issue":{"number":5}}`,
			want:       "",
		},
		{
			name:       "parent directory name is unresolved",
			event:      "issues",
			repository: "goravel/..",
			payload:    `{"issue":{"number":5}}`,
			want:       "",
		},
		{
			name:       "parent directory owner is unresolved",
			event:      "issues",
			repository: "../goraveler",
			payload:    `{"issue":{"number":5}}`,
			want:       "",
		},
		{
			name:       "current directory name is unresolved",
			event:      "issues",
			repository: "goravel/.",
			payload:    `{"issue":{"number":5}}`,
			want:       "",
		},
		{
			name:       "current directory owner is unresolved",
			event:      "issues",
			repository: "./goraveler",
			payload:    `{"issue":{"number":5}}`,
			want:       "",
		},
		{
			name:       "backslash repository is unresolved",
			event:      "issues",
			repository: `goravel\..\..\evil`,
			payload:    `{"issue":{"number":5}}`,
			want:       "",
		},
		{
			name:       "newline repository is unresolved",
			event:      "issues",
			repository: "goravel/f\nPR: x",
			payload:    `{"issue":{"number":5}}`,
			want:       "",
		},
		{
			name:       "unicode line separator repository is unresolved",
			event:      "issues",
			repository: "goravel/f\u2028PR: x",
			payload:    `{"issue":{"number":5}}`,
			want:       "",
		},
		{
			name:       "unicode line separator payload url falls back to constructed url",
			event:      "issues",
			repository: "goravel/framework",
			payload:    "{\"issue\":{\"number\":5,\"html_url\":\"https://github.com/goravel/framework/issues/5\u2028PR: https://evil.example/1\"}}",
			want:       "https://github.com/goravel/framework/issues/5",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := &event{Event: test.event, Repository: test.repository, Payload: test.payload}
			require.Equal(t, test.want, event.targetURL())
		})
	}
}
