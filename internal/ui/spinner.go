package ui

import (
	"fmt"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var spinnerStyle = lipgloss.NewStyle().Foreground(AccentColor)

// Progress-block frames — subtle sliding-fill animation. Less busy than a
// rotating dot, and matches the chunky aesthetic of the banner.
var frames = []string{"▱▱▱", "▰▱▱", "▰▰▱", "▰▰▰", "▰▰▱", "▰▱▱"}

// Spinner shows a non-blocking animated spinner on stdout. Suitable for
// wrapping long API calls so the terminal doesn't look frozen.
type Spinner struct {
	mu       sync.Mutex
	message  string
	stop     chan struct{}
	done     sync.WaitGroup
	stopOnce sync.Once
}

func NewSpinner(message string) *Spinner {
	return &Spinner{
		message: message,
		stop:    make(chan struct{}),
	}
}

// SetMessage updates the text shown next to the spinner. Safe to call from
// other goroutines while the spinner runs — used to show live progress
// (e.g. "5/41") as concurrent work completes.
func (s *Spinner) SetMessage(message string) {
	s.mu.Lock()
	s.message = message
	s.mu.Unlock()
}

// getMessage reads the current message under the lock so the animation
// goroutine never races a concurrent SetMessage.
func (s *Spinner) getMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.message
}

// Start begins the animation in a goroutine. Call Stop to end it.
func (s *Spinner) Start() {
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		i := 0
		for {
			select {
			case <-s.stop:
				fmt.Print("\r\033[K") // erase the spinner line
				return
			default:
				frame := spinnerStyle.Render(frames[i%len(frames)])
				fmt.Printf("\r\033[K%s %s", frame, s.getMessage())
				i++
				time.Sleep(150 * time.Millisecond)
			}
		}
	}()
}

// Stop halts the animation and blocks until the goroutine has exited, so
// the caller's next stdout write doesn't race with a final frame.
func (s *Spinner) Stop() {
	s.stopOnce.Do(func() {
		close(s.stop)
	})
	s.done.Wait()
}
