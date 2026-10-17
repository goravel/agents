package agent

import (
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
