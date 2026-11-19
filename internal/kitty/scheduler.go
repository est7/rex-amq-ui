package kitty

import "time"

// Animator is what the Scheduler paces: a picture that may change with
// time alone.
type Animator interface{ NeedsAnimation() bool }

// Tick is one firing of a frame loop. A tick already fired, or one from a
// loop that was stopped or replaced, is stale and draws nothing.
type Tick struct{ gen uint64 }

// MaxFPS caps Kitty animation frame rate to bound /tmp frame churn.
const MaxFPS = 15

// Scheduler decides when the next animation frame is due, at up to MaxFPS,
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

// NewScheduler paces frames at fps, held to 1..MaxFPS.
func NewScheduler(fps int) *Scheduler {
	return &Scheduler{interval: time.Second / time.Duration(min(max(fps, 1), MaxFPS))}
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
// frame now; again whether to arm next, the loop's next tick, to fire after
// delay. Each tick fires once: a late duplicate draws nothing. The loop's
// last frame is the one drawn when a stopped needing animation, so the
// settled picture shows.
func (s *Scheduler) Fire(t Tick, a Animator, now time.Time) (render bool, next Tick, delay time.Duration, again bool) {
	if !s.Owns(t) {
		return false, Tick{}, 0, false
	}
	s.gen++
	s.last = now
	if !a.NeedsAnimation() {
		s.pending = false
		return true, Tick{}, 0, false
	}
	return true, Tick{s.gen}, s.interval, true
}

// Stop ends the frame loop (pause, minimize, leaving the tab); its
// pending tick turns stale.
func (s *Scheduler) Stop() {
	s.pending = false
	s.gen++
}

// Owns reports whether t is the running loop's tick. A caller checks it
// before acting on a tick, so a stale one changes nothing.
func (s *Scheduler) Owns(t Tick) bool { return s.pending && t.gen == s.gen }
