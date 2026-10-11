// Package sched is the server's single background goroutine (PLAN §2): one
// ticker loop running every periodic job — today the hourly retention
// pruner (§6.3) and the 30 s public-page refresh. Alerting and the dead-man
// heartbeat join here in Phase 2 (§6.6); the Job shape does not change when
// they do.
package sched

import (
	"context"
	"log/slog"
	"time"
)

// Job is one periodic task.
type Job struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
}

// Run executes jobs sequentially on this goroutine until ctx is cancelled.
// Sequential on purpose: jobs are cheap (one SQL select + file deletes) and
// a second goroutine would only add concurrency the plan doesn't need.
// First runs are staggered by position so startup isn't a thundering herd.
func Run(ctx context.Context, log *slog.Logger, jobs ...Job) {
	if log == nil {
		log = slog.Default()
	}
	if len(jobs) == 0 {
		<-ctx.Done()
		return
	}
	next := make([]time.Time, len(jobs))
	t0 := time.Now()
	for i := range jobs {
		next[i] = t0.Add(time.Duration(i+1) * time.Second)
	}
	for {
		wait := time.Hour
		now := time.Now()
		for _, t := range next {
			if d := t.Sub(now); d < wait {
				wait = d
			}
		}
		if wait < 0 {
			wait = 0
		}
		// A stopped timer per wait, never time.After: the loop can spin at
		// sub-second resolution while a job overruns its schedule, and
		// time.After would leak a timer per iteration until it fired.
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		for _, i := range advance(time.Now(), next, jobs) {
			runJob(ctx, log, jobs[i])
		}
	}
}

// advance decides which jobs are due at cur and reschedules them. Pure so
// the scheduling math is testable without a clock (the goroutine around it
// is three lines of waiting). The following run is scheduled BEFORE the
// work so slow jobs don't drift (same rule as the firmware timer).
func advance(cur time.Time, next []time.Time, jobs []Job) []int {
	var fire []int
	for i := range jobs {
		if cur.Before(next[i]) {
			continue
		}
		next[i] = cur.Add(jobs[i].Every)
		fire = append(fire, i)
	}
	return fire
}

func runJob(ctx context.Context, log *slog.Logger, j Job) {
	jctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := j.Run(jctx); err != nil {
		log.Error("scheduled job failed", "job", j.Name, "err", err.Error())
	}
}
