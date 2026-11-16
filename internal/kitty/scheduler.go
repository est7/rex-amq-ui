package kitty

import "time"

// Animator is what the Scheduler paces: a picture that may change with
// time alone.
type Animator interface{ NeedsAnimation() bool }

// Tick names one frame loop. A tick from a loop that was stopped or
// replaced is stale and draws nothing.
type Tick struct{ gen uint64 }

// Scheduler decides when the next animation frame is due, at 24-30 fps,
// and only while the Animator needs one: an idle graph costs no frames.
// It never sleeps or starts goroutines; the caller arms a timer for the
// delay it returns (a tea.Tick in the UI) and calls Fire when it goes off.
// Time comes in as arguments, so tests drive it without a clock.
type Scheduler struct {
	interval time.Duration
	gen      uint64
	pending  bool
	last     time.Time
}

// NewScheduler paces frames at fps, held to 24..30.
func NewScheduler(fps int) *Scheduler {
	return &Scheduler{interval: time.Second / time.Duration(min(max(fps, 24), 30))}
}

// Interval is the time between frames.
func (s *Scheduler) Interval() time.Duration { return s.interval }

// Kick is called when something changed. It starts a frame loop when none
// runs and a needs animating, and returns the loop's tick and how long to
// wait before firing it (what remains of the interval since the last
// frame). ok is false when no new loop starts.
func (s *Scheduler) Kick(a Animator, now time.Time) (t Tick, delay time.Duration, ok bool) {
	if s.pending || !a.NeedsAnimation() {
		return Tick{}, 0, false
	}
	s.gen++
	s.pending = true
	return Tick{s.gen}, max(s.last.Add(s.interval).Sub(now), 0), true
}

// Fire is called when t's timer goes off. render says whether to draw a
// frame now; again whether to fire t once more after next. The loop's
// last frame is the one drawn when a stopped needing animation, so the
// settled picture shows.
func (s *Scheduler) Fire(t Tick, a Animator, now time.Time) (render bool, next time.Duration, again bool) {
	if !s.pending || t.gen != s.gen {
		return false, 0, false
	}
	s.last = now
	if !a.NeedsAnimation() {
		s.pending = false
		return true, 0, false
	}
	return true, s.interval, true
}

// Stop ends the frame loop (pause, minimize, leaving the tab); its
// pending tick turns stale.
func (s *Scheduler) Stop() {
	s.pending = false
	s.gen++
}
