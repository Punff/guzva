package sched

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// The scheduling math, purely: a job fires exactly when `cur` has reached
// its next slot, and a fired job reschedules from `cur` — a slow job that
// overruns its Every therefore does NOT drift into a burst (next run is a
// full Every after the fire decision, and skipped slots are skipped, not
// replayed).
func TestAdvanceFiresDueJobsOnly(t *testing.T) {
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	jobs := []Job{
		{Name: "a", Every: 30 * time.Second},
		{Name: "b", Every: time.Hour},
	}
	next := []time.Time{t0.Add(time.Second), t0.Add(3 * time.Second)}

	// Before either slot: nothing fires.
	if got := advance(t0, next, jobs); len(got) != 0 {
		t.Fatalf("t0 fired %v, want none", got)
	}
	// Slot a passes; only a fires and reschedules a full Every ahead.
	got := advance(t0.Add(2*time.Second), next, jobs)
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("fired %v, want [0]", got)
	}
	if want := t0.Add(32 * time.Second); !next[0].Equal(want) {
		t.Errorf("a rescheduled to %v, want %v", next[0], want)
	}
	// An hour later both are due; both fire, each from the same cur.
	got = advance(t0.Add(time.Hour), next, jobs)
	if len(got) != 2 {
		t.Fatalf("fired %v, want both", got)
	}
	if want := t0.Add(time.Hour + 30*time.Second); !next[0].Equal(want) {
		t.Errorf("a rescheduled to %v, want %v", next[0], want)
	}
}

// Smoke test for the goroutine: real clock, tiny Every, cancel stops it.
func TestRunExecutesAndStops(t *testing.T) {
	var runs atomic.Int64
	done := make(chan struct{})
	job := Job{
		Name:  "fast",
		Every: 2 * time.Millisecond,
		Run: func(ctx context.Context) error {
			if runs.Add(1) == 3 {
				close(done)
			}
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, nil, job)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job never ran three times")
	}
	cancel()
	// Run must return promptly after cancel (the timer is stopped, not
	// leaked into a long sleep).
	end := time.After(2 * time.Second)
	select {
	case <-end:
		t.Fatal("Run did not return after cancellation")
	default:
	}
}
