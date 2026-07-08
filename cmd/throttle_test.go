package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestThrottleStatusInactiveIsEmpty(t *testing.T) {
	if got := throttleStatus(0, 12*time.Second, 20*time.Second, 2, 5); got != "" {
		t.Errorf("active=0 should be empty, got %q", got)
	}
	if got := throttleStatus(-1, 12*time.Second, 20*time.Second, 2, 5); got != "" {
		t.Errorf("active<0 should be empty, got %q", got)
	}
}

func TestThrottleStatusActive(t *testing.T) {
	got := throttleStatus(3, 12*time.Second, 20*time.Second, 2, 5)
	for _, want := range []string{"rate-limited", "retry 2/5", "3 waiting", "12s → retry"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q missing %q", got, want)
		}
	}
	// frac = (20-12)/20 = 0.4 → round(0.4*8) = 3 filled cells.
	if filled := strings.Count(got, "▰"); filled != 3 {
		t.Errorf("status %q has %d filled cells, want 3", got, filled)
	}
	// Leading-space separator so it appends cleanly onto a base message.
	if !strings.HasPrefix(got, " ") {
		t.Errorf("status %q should start with a space separator", got)
	}
}

func TestThrottleStatusRoundsUpSeconds(t *testing.T) {
	// 11.2s remaining → ceil = 12s.
	got := throttleStatus(1, 11200*time.Millisecond, 20*time.Second, 1, 5)
	if !strings.Contains(got, "12s → retry") {
		t.Errorf("status %q should ceil remaining to 12s", got)
	}
}

func TestThrottleStatusZeroTotalNoPanic(t *testing.T) {
	// total<=0 → frac 0, no division by zero, no filled cells.
	got := throttleStatus(2, 5*time.Second, 0, 1, 5)
	if filled := strings.Count(got, "▰"); filled != 0 {
		t.Errorf("total=0 status %q should have 0 filled cells, got %d", got, filled)
	}
	if !strings.Contains(got, "2 waiting") {
		t.Errorf("status %q missing waiting count", got)
	}
}
