package core

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Run calls pass immediately and then after each interval plus jitter.
// A pass that is still running delays the next one instead of overlapping it.
func Run(ctx context.Context, interval, jitter time.Duration, log *slog.Logger, pass func(context.Context) error) error {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	for {
		if err := pass(ctx); err != nil && ctx.Err() == nil {
			log.Error("reconcile", "err", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(interval + jitterDelay(jitter))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func jitterDelay(jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(jitter) + 1))
}
