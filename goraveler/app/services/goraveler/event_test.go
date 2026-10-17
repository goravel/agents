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
		Repository: "goravel/goravel",
		Payload:    `{"number":123,"comment":{"id":24680,"body":"@goraveler review"}}`,
	}

	want := "Repository: goravel/goravel\n" +
		"Event: issue_comment\n" +
		"Comment ID: 24680\n" +
		"Number: #123\n\n" +
		"Instruction:\n" +
		"review"
	require.Equal(t, want, event.prompt("review"))
}

func TestEventPromptWithoutComment(t *testing.T) {
	event := &event{
		Event:      "issues",
		Repository: "goravel/framework",
		Payload:    `{"issue":{"number":5,"body":"@goraveler plan"}}`,
	}

	want := "Repository: goravel/framework\n" +
		"Event: issues\n" +
		"Number: #5\n\n" +
		"Instruction:\n" +
		"plan"
	require.Equal(t, want, event.prompt("plan"))
}
