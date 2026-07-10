package ui

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

var spinnerStyle = lipgloss.NewStyle().Foreground(AccentColor)

// Progress-block frames — subtle sliding-fill animation. Less busy than a
// rotating dot, and matches the chunky aesthetic of the banner.
var frames = []string{"▱▱▱", "▰▱▱", "▰▰▱", "▰▰▰", "▰▰▱", "▰▱▱"}

// Spinner shows a non-blocking animated spinner on stdout. Suitable for
// wrapping long API calls so the terminal doesn't look frozen.
type Spinner struct {
	mu        sync.Mutex
	message   string
	messageFn func() string
	stop      chan struct{}
	done      sync.WaitGroup
	stopOnce  sync.Once
}

func NewSpinner(message string) *Spinner {
	return &Spinner{
		message: message,
		stop:    make(chan struct{}),
	}
}

// SetMessageFunc installs a provider that the animation goroutine calls on
// every frame to compute the live message. Use it when the text must stay fresh
// even while the caller's own goroutines are stalled (e.g. all workers sleeping
// on a 429) — the spinner's repaint drives it, so no external timer is needed.
// A nil provider falls back to the static message set by NewSpinner.
func (s *Spinner) SetMessageFunc(fn func() string) {
	s.mu.Lock()
	s.messageFn = fn
	s.mu.Unlock()
}

// getMessage reads the current message under the lock so the animation
// goroutine never races a concurrent update. A provider (SetMessageFunc),
// if set, wins; it's snapshotted under the lock and called outside it so an
// arbitrary callback never runs while the mutex is held.
func (s *Spinner) getMessage() string {
	s.mu.Lock()
	fn, msg := s.messageFn, s.message
	s.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return msg
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
				// The repaint (\r\033[K) can only erase the CURRENT terminal row.
				// A message wider than the terminal wraps onto a second row, the
				// cursor lands there, and every frame leaves the previous row
				// behind — one stale spinner line per tick. Truncate (ANSI-aware)
				// to the live width so the line never wraps.
				msg := s.getMessage()
				if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 6 {
					msg = ansi.Truncate(msg, w-5, "…") // frame (3) + space + spare column
				}
				fmt.Printf("\r\033[K%s %s", frame, msg)
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
