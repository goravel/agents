package agent

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
			"payload": "{\"number\":123,\"comment\":{\"id\":24680,\"body\":\"@goravel-agent review\"}}"
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

	instruction, ok := env.Data[0].instruction(DefaultMention)
	require.True(t, ok)
	require.Equal(t, "review", instruction)

	_, ok = env.Data[1].instruction(DefaultMention)
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
			payload: `{"number":1,"comment":{"id":9,"body":"@goravel-agent plan this"}}`,
			want:    "plan this",
			ok:      true,
		},
		{
			name:    "issue body",
			payload: `{"number":2,"issue":{"number":2,"body":"@goravel-agent implement"}}`,
			want:    "implement",
			ok:      true,
		},
		{
			name:    "pull request body",
			payload: `{"number":3,"pull_request":{"number":3,"body":"@goravel-agent review please"}}`,
			want:    "review please",
			ok:      true,
		},
		{
			name:    "mention without instruction",
			payload: `{"number":4,"comment":{"id":1,"body":"hi @goravel-agent"}}`,
			ok:      false,
		},
		{
			name:    "longer handle is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"@goravel-agent2 review"}}`,
			ok:      false,
		},
		{
			name:    "prefixed word is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"ping bob@goravel-agent.com please"}}`,
			ok:      false,
		},
		{
			name:    "hyphen before handle is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"ping bob-@goravel-agent.com please"}}`,
			ok:      false,
		},
		{
			name:    "hyphenated suffix is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"@goravel-agent-review please"}}`,
			ok:      false,
		},
		{
			name:    "pluralised handle is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"@goravel-agents review"}}`,
			ok:      false,
		},
		{
			name:    "org prefix alone is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"@goravel review"}}`,
			ok:      false,
		},
		{
			name:    "underscored handle is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"@goravel_agent review"}}`,
			ok:      false,
		},
		{
			name:    "space separated handle is not a mention",
			payload: `{"number":4,"comment":{"id":1,"body":"@goravel agent review"}}`,
			ok:      false,
		},
		{
			name:    "valid mention after a rejected one",
			payload: `{"number":4,"comment":{"id":1,"body":"@goravel-agent2 then @goravel-agent review"}}`,
			want:    "review",
			ok:      true,
		},
		{
			name:    "handle followed by punctuation",
			payload: `{"number":4,"comment":{"id":1,"body":"@goravel-agent, please review"}}`,
			want:    "please review",
			ok:      true,
		},
		{
			name:    "bare decorated mention is not an instruction",
			payload: `{"number":4,"comment":{"id":1,"body":"**@goravel-agent**"}}`,
			ok:      false,
		},
		{
			name:    "case-insensitive handle",
			payload: `{"number":4,"comment":{"id":1,"body":"@Goravel-Agent review"}}`,
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
			got, ok := event.instruction(DefaultMention)
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

// TestEventReaction pins which comment the eyes reaction targets: conversation
// comments go through the issues collection, inline review comments through the
// pull request collection, and a body-only mention has no comment at all.
func TestEventReaction(t *testing.T) {
	tests := []struct {
		name           string
		event          string
		payload        string
		wantCollection string
		wantID         int64
		wantOK         bool
	}{
		{
			name:           "conversation comment",
			event:          "issue_comment",
			payload:        `{"number":123,"comment":{"id":24680,"body":"@goravel-agent review"}}`,
			wantCollection: reactionCollectionIssue,
			wantID:         24680,
			wantOK:         true,
		},
		{
			name:           "inline review comment",
			event:          "pull_request_review_comment",
			payload:        `{"pull_request":{"number":123},"comment":{"id":98765,"body":"@goravel-agent review"}}`,
			wantCollection: reactionCollectionReview,
			wantID:         98765,
			wantOK:         true,
		},
		{
			name:    "issue body only",
			event:   "issues",
			payload: `{"number":5,"issue":{"number":5,"body":"@goravel-agent review"}}`,
		},
		{
			name:    "invalid payload",
			event:   "issue_comment",
			payload: `not json`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := &event{Event: test.event, Payload: test.payload}

			collection, commentID, ok := event.reaction()
			require.Equal(t, test.wantOK, ok)
			require.Equal(t, test.wantCollection, collection)
			require.Equal(t, test.wantID, commentID)
		})
	}
}

func TestEventPrompt(t *testing.T) {
	event := &event{
		Event:      "issue_comment",
		Repository: "goravel/goravel",
		Payload: `{"number":123,` +
			`"pull_request":{"number":123,"html_url":"https://github.com/goravel/goravel/pull/123"},` +
			`"comment":{"id":24680,"html_url":"https://github.com/goravel/goravel/pull/123#issuecomment-24680",` +
			`"body":"@goravel-agent review"}}`,
	}

	want := "Repository: goravel/goravel\n" +
		"PR: https://github.com/goravel/goravel/pull/123\n" +
		"comment: https://github.com/goravel/goravel/pull/123#issuecomment-24680\n" +
		"Instruction: review"
	require.Equal(t, want, event.prompt("review", "goravel/goravel"))
}

func TestEventAgentPrompt(t *testing.T) {
	event := &event{
		Event:      "issue_comment",
		Repository: "goravel/goravel",
		Payload: `{"number":123,"pull_request":{"number":123,"html_url":"https://github.com/goravel/goravel/pull/123"},` +
			`"comment":{"id":24680,"html_url":"https://github.com/goravel/goravel/pull/123#issuecomment-24680","body":"@goravel-agent review"}}`,
	}

	body := "PR: https://github.com/goravel/goravel/pull/123\n" +
		"comment: https://github.com/goravel/goravel/pull/123#issuecomment-24680\n" +
		"Instruction: review"
	want := body + "\n" +
		"comment the review result to the pr"

	got := event.agentPrompt(agentReview, "review")
	require.Equal(t, want, got)
	require.NotContains(t, got, "Repository:")

	// A non-review agent on the same pull request keeps the bare context block,
	// so the review-only gate cannot silently widen to every agent on a PR.
	require.Equal(t, body, event.agentPrompt(agentShip, "review"))
}

// TestEventAgentPromptReviewOnIssue pins that a review mention on a plain issue
// is not steered: only a pull request gets the post-to-PR line.
func TestEventAgentPromptReviewOnIssue(t *testing.T) {
	event := &event{
		Event:      "issue_comment",
		Repository: "goravel/goravel",
		Payload:    `{"number":45,"comment":{"id":24680,"body":"@goravel-agent review"}}`,
	}

	want := "Issue: https://github.com/goravel/goravel/issues/45\n" +
		"comment: https://github.com/goravel/goravel/issues/45#issuecomment-24680\n" +
		"Instruction: review"
	require.Equal(t, want, event.agentPrompt(agentReview, "review"))
}

func TestEventPromptWithoutComment(t *testing.T) {
	event := &event{
		Event:      "issues",
		Repository: "goravel/framework",
		Payload: `{"issue":{"number":5,"html_url":"https://github.com/goravel/framework/issues/5",` +
			`"body":"@goravel-agent plan"}}`,
	}

	want := "Repository: goravel/framework\n" +
		"Issue: https://github.com/goravel/framework/issues/5\n" +
		"Instruction: plan"
	require.Equal(t, want, event.prompt("plan", "goravel/framework"))
}

// TestEventPromptTargetRepository pins that the Repository line shows the
// router-chosen target while the links keep pointing at the source repository
// where the mention was posted.
func TestEventPromptTargetRepository(t *testing.T) {
	event := &event{
		Event:      "issues",
		Repository: "goravel/goravel",
		Payload: `{"issue":{"number":45,"html_url":"https://github.com/goravel/goravel/issues/45",` +
			`"body":"@goravel-agent plan"}}`,
	}

	want := "Repository: goravel/framework\n" +
		"Issue: https://github.com/goravel/goravel/issues/45\n" +
		"Instruction: plan"
	require.Equal(t, want, event.prompt("plan", "goravel/framework"))
}

// TestEventPromptMutuallyExclusiveLinks pins that a pull request shows only the
// PR link and an issue shows only the Issue link.
func TestEventPromptMutuallyExclusiveLinks(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantLink    string
		wantComment string
		omitIssues  bool
		omitPR      bool
	}{
		{
			name: "pull request",
			payload: `{"pull_request":{"number":123,"html_url":"https://github.com/goravel/goravel/pull/123"},` +
				`"comment":{"id":1,"body":"@goravel-agent review"}}`,
			wantLink:    "PR: https://github.com/goravel/goravel/pull/123\n",
			wantComment: "comment: https://github.com/goravel/goravel/pull/123#issuecomment-1\n",
			omitIssues:  true,
		},
		{
			name: "issue",
			payload: `{"issue":{"number":45,"html_url":"https://github.com/goravel/goravel/issues/45",` +
				`"body":"@goravel-agent review"}}`,
			wantLink: "Issue: https://github.com/goravel/goravel/issues/45\n",
			omitPR:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := &event{
				Repository: "goravel/goravel",
				Payload:    test.payload,
			}

			got := event.prompt("review", "goravel/goravel")
			require.Contains(t, got, test.wantLink)
			if test.wantComment != "" {
				require.Contains(t, got, test.wantComment)
			}
			if test.omitIssues {
				require.NotContains(t, got, "Issue:")
			}
			if test.omitPR {
				require.NotContains(t, got, "PR:")
			}
		})
	}
}

// TestEventPromptPullRequestLinks covers the two PR shapes that carry no
// top-level pull_request.html_url: an issue_comment on a pull request (nested
// issue.pull_request) and a pull request with no html_url at all (constructed
// fallback).
func TestEventPromptPullRequestLinks(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name: "issue comment on a pull request",
			payload: `{"issue":{"number":45,"html_url":"https://github.com/goravel/goravel/pull/45",` +
				`"pull_request":{"html_url":"https://github.com/goravel/goravel/pull/45"},` +
				`"body":"@goravel-agent review"}}`,
			want: "Repository: goravel/goravel\n" +
				"PR: https://github.com/goravel/goravel/pull/45\n" +
				"Instruction: review",
		},
		{
			name:    "pull request without html_url",
			payload: `{"pull_request":{"number":123},"comment":{"id":24680,"body":"@goravel-agent review"}}`,
			want: "Repository: goravel/goravel\n" +
				"PR: https://github.com/goravel/goravel/pull/123\n" +
				"comment: https://github.com/goravel/goravel/pull/123#issuecomment-24680\n" +
				"Instruction: review",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := &event{Repository: "goravel/goravel", Payload: test.payload}
			require.Equal(t, test.want, event.prompt("review", "goravel/goravel"))
		})
	}
}

// TestEventPromptFallbackLinks pins the constructed links used when the payload
// carries no html_url fields.
func TestEventPromptFallbackLinks(t *testing.T) {
	event := &event{
		Event:      "issue_comment",
		Repository: "goravel/goravel",
		Payload:    `{"number":45,"comment":{"id":24680,"body":"@goravel-agent review"}}`,
	}

	want := "Repository: goravel/goravel\n" +
		"Issue: https://github.com/goravel/goravel/issues/45\n" +
		"comment: https://github.com/goravel/goravel/issues/45#issuecomment-24680\n" +
		"Instruction: review"
	require.Equal(t, want, event.prompt("review", "goravel/goravel"))
}
