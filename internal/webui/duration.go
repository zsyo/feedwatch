//go:build webui

package webui

import (
	"strings"
	"time"
)

// CompactDuration renders a duration the way the settings page shows it,
// trimming the trailing zero units Go always prints ("30m0s" -> "30m",
// "2h0m0s" -> "2h"). Only whole-unit suffixes are dropped, so "20s" and
// "250ms" keep their final zero.
func CompactDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
