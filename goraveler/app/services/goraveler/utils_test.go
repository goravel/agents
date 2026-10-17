package goraveler

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
