package agent

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetryKey(t *testing.T) {
	require.Equal(t, "d-1", retryKey(&event{ID: 7, DeliveryID: "d-1"}))
	require.Equal(t, "7", retryKey(&event{ID: 7}))
}

func TestParseSenders(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]struct{}
	}{
		{name: "empty", raw: "", want: map[string]struct{}{}},
		{name: "blank entries", raw: "  ,  ", want: map[string]struct{}{}},
		{name: "single sender", raw: "bowen", want: map[string]struct{}{"bowen": {}}},
		{name: "trims and lowercases", raw: " Bowen , Alice ", want: map[string]struct{}{"bowen": {}, "alice": {}}},
		{name: "ignores blank entries", raw: "bowen,, alice,", want: map[string]struct{}{"bowen": {}, "alice": {}}},
		{name: "collapses duplicates", raw: "bowen, BowEn", want: map[string]struct{}{"bowen": {}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, parseSenders(test.raw))
		})
	}
}

func TestNormalizeMention(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty falls back to the default", raw: "", want: DefaultMention},
		{name: "blank falls back to the default", raw: "   ", want: DefaultMention},
		{name: "keeps the leading at sign", raw: "@bot", want: "@bot"},
		{name: "adds the leading at sign", raw: "bot", want: "@bot"},
		{name: "trims without changing case", raw: " @Goravel-Agent ", want: "@Goravel-Agent"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, normalizeMention(test.raw))
		})
	}
}

func TestShellCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "safe tokens pass through", args: []string{"paseo", "run", "--provider", "opencode"}, want: "paseo run --provider opencode"},
		{name: "end of options marker", args: []string{"--no-wait", "--", "do it"}, want: "--no-wait -- 'do it'"},
		{name: "punctuation stays bare including mid-token equals", args: []string{"--model", "opencode-go/deepseek-v4.1-flash", "k=v:a,b@c%d+e"}, want: "--model opencode-go/deepseek-v4.1-flash k=v:a,b@c%d+e"},
		{name: "leading equals is quoted", args: []string{"=ls", "--model=opencode-go/deepseek-v4.1-flash"}, want: "'=ls' --model=opencode-go/deepseek-v4.1-flash"},
		{name: "empty string is quoted", args: []string{"--title", ""}, want: "--title ''"},
		{name: "spaces are quoted", args: []string{"--title", "Review PR 258 in goravel/gin"}, want: "--title 'Review PR 258 in goravel/gin'"},
		{name: "single quote is escaped", args: []string{"it's"}, want: `'it'\''s'`},
		{name: "single quote at both ends", args: []string{"'quoted'"}, want: `''\''quoted'\'''`},
		{name: "single quote at the end", args: []string{"quoted'"}, want: `'quoted'\'''`},
		{name: "adjacent single quotes", args: []string{"a''b"}, want: `'a'\'''\''b'`},
		{name: "double quotes are quoted", args: []string{`say "hi"`}, want: `'say "hi"'`},
		{name: "expansion is quoted", args: []string{"$HOME", "*.go", "~", `a\b`}, want: `'$HOME' '*.go' '~' 'a\b'`},
		{name: "non-ascii is quoted", args: []string{"héllo"}, want: "'héllo'"},
		{name: "multi-line prompt stays quoted", args: []string{"--", "PR: https://github.com/goravel/gin/pull/258\nInstruction: review it"}, want: "-- 'PR: https://github.com/goravel/gin/pull/258\nInstruction: review it'"},
		{name: "empty argv", args: nil, want: ""},
		{
			name: "full dispatcher command",
			args: []string{
				"paseo", "run", "--provider", "opencode", "--mode", "review",
				"--model", "opencode-go/deepseek-v4.1-flash", "--thinking", "high",
				"--title", "Review PR 258 in goravel/gin", "--no-wait", "--",
				"PR: https://github.com/goravel/gin/pull/258\nInstruction: review it",
			},
			want: "paseo run --provider opencode --mode review " +
				"--model opencode-go/deepseek-v4.1-flash --thinking high " +
				"--title 'Review PR 258 in goravel/gin' --no-wait -- " +
				"'PR: https://github.com/goravel/gin/pull/258\nInstruction: review it'",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, shellCommand(test.args))
		})
	}

	// The cases above compare against hand-written strings; this one checks the
	// actual promise behind them — that a shell splits the logged line back into
	// the same argv — so an edit to shellSafeRunes breaks a test instead of only
	// breaking an operator's paste. `printf '%s\0'` keeps an empty argument
	// visible and `dummy` stops a leading `--` from being read as a printf flag.
	t.Run("round trips through sh", func(t *testing.T) {
		sh, err := exec.LookPath("sh")
		if err != nil {
			t.Skip("sh is not available")
		}

		for _, test := range tests {
			out, err := exec.Command(sh, "-c", "printf '%s\\0' dummy "+shellCommand(test.args)).Output()
			require.NoError(t, err, test.name)

			want := "dummy\x00"
			for _, arg := range test.args {
				want += arg + "\x00"
			}
			require.Equal(t, want, string(out), test.name)
		}
	})
}
