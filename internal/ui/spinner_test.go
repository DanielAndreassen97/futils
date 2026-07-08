package ui

import (
	"sync"
	"testing"
)

func TestSpinnerSetMessageFuncUpdates(t *testing.T) {
	s := NewSpinner("Comparing 0/41...")
	if got := s.getMessage(); got != "Comparing 0/41..." {
		t.Fatalf("initial message = %q", got)
	}
	s.SetMessageFunc(func() string { return "Comparing 5/41..." })
	if got := s.getMessage(); got != "Comparing 5/41..." {
		t.Errorf("after SetMessageFunc = %q, want %q", got, "Comparing 5/41...")
	}
}

// Concurrent SetMessageFunc/getMessage must not race (run with -race).
func TestSpinnerSetMessageFuncConcurrentSafe(t *testing.T) {
	s := NewSpinner("start")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.SetMessageFunc(func() string { return "x" })
			_ = s.getMessage()
		}()
	}
	wg.Wait()
}

func TestSpinnerSetMessageFuncWins(t *testing.T) {
	s := NewSpinner("static")
	s.SetMessageFunc(func() string { return "dynamic" })
	if got := s.getMessage(); got != "dynamic" {
		t.Errorf("provider should win over static message, got %q", got)
	}
	s.SetMessageFunc(nil)
	if got := s.getMessage(); got != "static" {
		t.Errorf("nil provider should fall back to static message, got %q", got)
	}
}

// The animation goroutine calls the provider on every frame while another
// goroutine swaps it — the snapshot-outside-lock path must not race (-race).
func TestSpinnerSetMessageFuncConcurrent(t *testing.T) {
	s := NewSpinner("start")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.SetMessageFunc(func() string { return "x" })
			_ = s.getMessage()
		}()
	}
	wg.Wait()
}
