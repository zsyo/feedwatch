//go:build webui

package webui

import (
	"testing"
	"time"
)

// CompactDuration feeds the settings page, so it must not swallow a meaningful
// trailing zero: "20s" has to stay "20s", only whole-unit suffixes go.
func TestCompactDuration(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Minute:             "30m",
		2 * time.Hour:                "2h",
		7 * 24 * time.Hour:           "168h",
		time.Hour + 30*time.Minute:   "1h30m",
		time.Minute + 30*time.Second: "1m30s",
		20 * time.Second:             "20s",
		250 * time.Millisecond:       "250ms",
		1500 * time.Millisecond:      "1.5s",
		100 * time.Millisecond:       "100ms",
		0:                            "0s",
		5 * time.Minute:              "5m",
		time.Hour + 2*time.Minute + 3*time.Second: "1h2m3s",
	}
	for in, want := range cases {
		if got := CompactDuration(in); got != want {
			t.Errorf("CompactDuration(%v) = %q, want %q", in, got, want)
		}
	}
}
