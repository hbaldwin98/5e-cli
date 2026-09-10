package ask

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Variables rather than constants so tests can shrink batches (to get many
// of them from a small corpus) and skip real backoff sleeps.
var (
	// embedBatch bounds inputs per request.
	embedBatch = 64
	// embedConcurrency is how many embedding requests a cache build keeps in
	// flight at once. Requests used to go out strictly one after another,
	// so a build's wall time was the sum of every round trip; a few in
	// parallel cuts that without inviting the rate limiting a large fan-out
	// would.
	embedConcurrency = 4
	// embedAttempts is how many times one batch is tried before a transient
	// failure (rate limit, outage, dropped connection) fails the build.
	embedAttempts = 5
	// embedRetryDelay is the backoff before retry number attempt (1-based):
	// 1s, 2s, 4s, 8s.
	embedRetryDelay = func(attempt int) time.Duration { return time.Second << (attempt - 1) }
	// maxRetryWait caps a server's Retry-After, so a hostile or confused
	// header can't park the build for an hour.
	maxRetryWait = time.Minute
)

// embedConcurrently embeds chunks in batchEnd-sized batches, up to
// embedConcurrency requests at a time, calling onBatch with each finished
// batch and the running count of chunks done. onBatch runs only on the
// calling goroutine, one batch at a time, so it can write to a transaction
// that isn't safe for concurrent use; batches arrive in completion order.
//
// The first failure — a batch that exhausted its retries or an onBatch
// error — cancels every other request in flight, and that failure is what's
// returned, not the "context canceled" its siblings see as a result. When
// ctx itself is cancelled, ctx's error is returned.
func embedConcurrently(ctx context.Context, cli *client, chunks []chunk, onBatch func(batch []chunk, vecs [][]float32, done int) error) error {
	type span struct{ start, end int }
	var spans []span
	for i := 0; i < len(chunks); {
		end := batchEnd(chunks, i)
		spans = append(spans, span{i, end})
		i = end
	}

	buildCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		span span
		vecs [][]float32
		err  error
	}
	jobs := make(chan span)
	results := make(chan result)
	var wg sync.WaitGroup
	for range min(embedConcurrency, len(spans)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				batch := chunks[s.start:s.end]
				inputs := make([]string, len(batch))
				for j, ch := range batch {
					inputs[j] = ch.Text
				}
				vecs, err := embedWithRetry(buildCtx, cli, inputs)
				if err != nil {
					err = fmt.Errorf("embedding chunks %d-%d of %d: %w", s.start+1, s.end, len(chunks), err)
				}
				select {
				case results <- result{s, vecs, err}:
				case <-buildCtx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, s := range spans {
			select {
			case jobs <- s:
			case <-buildCtx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var firstErr error
	done := 0
	for r := range results {
		if firstErr != nil {
			continue // draining what was already in flight
		}
		if r.err == nil {
			done += r.span.end - r.span.start
			r.err = onBatch(chunks[r.span.start:r.span.end], r.vecs, done)
		}
		if r.err != nil {
			firstErr = r.err
			cancel()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return firstErr
}

// embedWithRetry is cli.Embed, retrying transient failures with backoff
// (honoring a server's Retry-After when it asks for longer). A request the
// server rejected outright — a bad key, an unknown model, oversize input —
// fails on the first attempt, since repeating it can't help.
func embedWithRetry(ctx context.Context, cli *client, inputs []string) ([][]float32, error) {
	for attempt := 1; ; attempt++ {
		vecs, err := cli.Embed(ctx, inputs)
		if err == nil {
			return vecs, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retryable(err) {
			return nil, err
		}
		if attempt >= embedAttempts {
			return nil, fmt.Errorf("gave up after %d attempts: %w", attempt, err)
		}
		wait := embedRetryDelay(attempt)
		var se *statusError
		if errors.As(err, &se) && se.RetryAfter > wait {
			wait = min(se.RetryAfter, maxRetryWait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// retryable reports whether an Embed error is worth another attempt: a
// rate limit or server-side status, or a transport failure (connection
// refused or reset, timeout, a body cut off mid-read).
func retryable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.transient()
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}
