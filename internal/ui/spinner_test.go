package ui

import (
	"sync"
	"testing"
)

func TestSpinnerSetMessageUpdates(t *testing.T) {
	s := NewSpinner("Comparing 0/41...")
	if got := s.getMessage(); got != "Comparing 0/41..." {
		t.Fatalf("initial message = %q", got)
	}
	s.SetMessage("Comparing 5/41...")
	if got := s.getMessage(); got != "Comparing 5/41..." {
		t.Errorf("after SetMessage = %q, want %q", got, "Comparing 5/41...")
	}
}

// Concurrent SetMessage/getMessage must not race (run with -race).
func TestSpinnerSetMessageConcurrent(t *testing.T) {
	s := NewSpinner("start")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.SetMessage("x")
			_ = s.getMessage()
		}()
	}
	wg.Wait()
}
