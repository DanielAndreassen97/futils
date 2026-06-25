package cmd

import (
	"fmt"
	"math"
	"time"

	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// liveThrottleStatus reads the live 429-backoff state via a single torn-free
// ThrottleSnapshot call and formats it for a spinner suffix. Both render
// closures (compare and publish) use this so the consistent-snapshot path is
// never bypassed.
func liveThrottleStatus() string {
	a, r, t, n := fabric.ThrottleSnapshot()
	return throttleStatus(a, r, t, n, fabric.MaxThrottleRetries())
}

// throttleStatus formats the live 429-backoff suffix appended onto a spinner's
// base message during a rate-limit stall. It's pure (no fabric globals) so the
// formatting is unit-testable; the wiring calls fabric.ThrottleSnapshot() once
// per frame and feeds the result in.
//
// active<=0 → "" (no throttling right now, append nothing). Otherwise it returns
// a leading-space-separated suffix: a green progress bar filling toward retry, a
// countdown, the retry attempt, and the count of requests currently waiting:
//
//	— rate-limited ▰▰▰▱▱▱▱▱ 12s → retry · retry 2/5 · 3 waiting
//
// frac (bar fill) is (total-remaining)/total clamped to [0,1], 0 when total<=0.
// The countdown is ceil(remaining) in whole seconds, floored at 0.
func throttleStatus(active int, remaining, total time.Duration, attempt, maxRetries int) string {
	if active <= 0 {
		return ""
	}
	var frac float64
	if total > 0 {
		frac = float64(total-remaining) / float64(total)
	}
	secs := int(math.Ceil(remaining.Seconds()))
	if secs < 0 {
		secs = 0
	}
	return fmt.Sprintf(" — rate-limited %s %ds → retry · retry %d/%d · %d waiting",
		ui.RenderBar(frac, 8), secs, attempt, maxRetries, active)
}
