// Package sched is the server's single background goroutine (PLAN §2): one
// ticker loop running periodic jobs — Phase 1: the retention pruner (§6.3).
// Freshness rollup, missed-poll detection and alerts join here in Phase 2
// (§6.6); the Job shape does not change when they do.
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
func Run(ctx context.Context, log *slog.Logger, jobs ...Job) {
	if log == nil {
		log = slog.Default()
	}
	if len(jobs) == 0 {
		<-ctx.Done()
		return
	}
	next := make([]time.Time, len(jobs))
	now := time.Now()
	for i := range jobs {
		// Stagger first runs by their order so startup isn't a thundering
		// herd on a small box.
		next[i] = now.Add(time.Duration(i+1) * time.Second)
	}
	for {
		wait := time.Hour
		for _, t := range next {
			if d := time.Until(t); d < wait {
				wait = d
			}
		}
		if wait < 0 {
			wait = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		now = time.Now()
		for i := range jobs {
			if now.Before(next[i]) {
				continue
			}
			// Schedule the following run before doing the work so slow
			// jobs don't drift (same rule as the firmware timer).
			next[i] = now.Add(jobs[i].Every)
			runJob(ctx, log, jobs[i])
		}
	}
}

func runJob(ctx context.Context, log *slog.Logger, j Job) {
	jctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := j.Run(jctx); err != nil {
		log.Error("scheduled job failed", "job", j.Name, "err", err.Error())
	}
}
