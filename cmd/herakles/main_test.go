package main

import (
	"io"
	"testing"
)

func TestExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{nil, 2},
		{[]string{"bogus"}, 2},
		{[]string{"validate", "-config", "../../internal/config/testdata/valid.yaml"}, 0},
		{[]string{"validate", "-config", "../../internal/config/testdata/invalid.yaml"}, 1},
		{[]string{"validate", "-config", "missing.yaml"}, 1},
		{[]string{"run", "-config", "../../internal/config/testdata/valid.yaml", "-target", "nope"}, 1},
		{[]string{"run", "-config", "../../internal/config/testdata/valid.yaml", "-log-level", "loud"}, 2},
	}
	for _, tc := range cases {
		if got := run(tc.args, io.Discard); got != tc.want {
			t.Errorf("run(%v) = %d, want %d", tc.args, got, tc.want)
		}
	}
}
