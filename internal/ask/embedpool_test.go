package ask

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withEmbedKnobs sets the build's batch size and turns off real backoff
// sleeps for one test, restoring both afterward.
func withEmbedKnobs(t *testing.T, batch int) {
	t.Helper()
	oldBatch, oldDelay := embedBatch, embedRetryDelay
	embedBatch = batch
	embedRetryDelay = func(int) time.Duration { return 0 }
	t.Cleanup(func() { embedBatch, embedRetryDelay = oldBatch, oldDelay })
}

// embedInterceptor serves /embeddings through intercept first; when it
// returns false the request falls through to the fake API as normal.
func embedInterceptor(api *fakeAPI, intercept func(w http.ResponseWriter, input string, call int32) bool) http.Handler {
	next := api.handler()
	var calls atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/embeddings" {
			body, _ := io.ReadAll(r.Body)
			if intercept(w, string(body), calls.Add(1)) {
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	})
}

func assertNoCacheLeftBehind(t *testing.T, cfg Config) {
	t.Helper()
	for _, p := range []string{cfg.CachePath, cfg.CachePath + ".tmp"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("a failed build must not leave %s behind: %v", p, err)
		}
	}
}

func TestBuild_retriesARateLimitThenSucceeds(t *testing.T) {
	withEmbedKnobs(t, 64)
	api := &fakeAPI{}
	st, cfg := harnessServing(t, embedInterceptor(api, func(w http.ResponseWriter, _ string, call int32) bool {
		if call <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
			return true
		}
		return false
	}))
	defer st.Close()

	hits, err := Retrieve(context.Background(), st, cfg, Query{Text: "fire explosion", Limit: 3})
	if err != nil {
		t.Fatalf("want two rate limits retried through, got %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("want hits once the build succeeds")
	}
}

func TestBuild_doesNotRetryARejectedRequest(t *testing.T) {
	withEmbedKnobs(t, 64)
	var calls atomic.Int32
	st, cfg := harnessServing(t, embedInterceptor(&fakeAPI{}, func(w http.ResponseWriter, _ string, _ int32) bool {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"model fake-embed does not exist"}}`)
		return true
	}))
	defer st.Close()

	_, err := Retrieve(context.Background(), st, cfg, Query{Text: "fire", Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "model fake-embed does not exist") {
		t.Fatalf("want the API's rejection surfaced, got %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a 400 can't succeed on retry; want 1 request, got %d", n)
	}
	assertNoCacheLeftBehind(t, cfg)
}

func TestBuild_givesUpAfterRepeatedOutages(t *testing.T) {
	withEmbedKnobs(t, 64)
	var calls atomic.Int32
	st, cfg := harnessServing(t, embedInterceptor(&fakeAPI{}, func(w http.ResponseWriter, _ string, _ int32) bool {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "upstream is down")
		return true
	}))
	defer st.Close()

	_, err := Retrieve(context.Background(), st, cfg, Query{Text: "fire", Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "upstream is down") || !strings.Contains(err.Error(), fmt.Sprintf("after %d attempts", embedAttempts)) {
		t.Fatalf("want the outage reported with the attempt count, got %v", err)
	}
	if n := calls.Load(); n != int32(embedAttempts) {
		t.Fatalf("want exactly %d attempts, got %d", embedAttempts, n)
	}
	assertNoCacheLeftBehind(t, cfg)
}

func TestBuild_keepsSeveralRequestsInFlight(t *testing.T) {
	withEmbedKnobs(t, 1) // one chunk per request: many batches from the small corpus
	var inFlight, peak atomic.Int32
	st, cfg := harnessServing(t, embedInterceptor(&fakeAPI{}, func(http.ResponseWriter, string, int32) bool {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		return false
	}))
	defer st.Close()

	var updates []EmbedProgress
	cfg.OnProgress = func(p EmbedProgress) { updates = append(updates, p) }
	if _, err := Retrieve(context.Background(), st, cfg, Query{Text: "fire", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if p := peak.Load(); p < 2 {
		t.Fatalf("want embedding requests overlapping, peak in flight was %d", p)
	}
	last := updates[len(updates)-1]
	if last.Done != last.Total {
		t.Fatalf("want the final update to report completion, got %+v", last)
	}
	for i := 1; i < len(updates); i++ {
		if updates[i].Done < updates[i-1].Done {
			t.Fatalf("progress should never go backward: %+v", updates)
		}
	}
}

func TestBuild_reportsTheFailingBatchNotItsCancelledSiblings(t *testing.T) {
	withEmbedKnobs(t, 1)
	st, cfg := harnessServing(t, embedInterceptor(&fakeAPI{}, func(w http.ResponseWriter, input string, _ int32) bool {
		if strings.Contains(input, "Longsword") || strings.Contains(input, "martial melee") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"input rejected"}}`)
			return true
		}
		time.Sleep(10 * time.Millisecond)
		return false
	}))
	defer st.Close()

	_, err := Retrieve(context.Background(), st, cfg, Query{Text: "fire", Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "input rejected") || !strings.Contains(err.Error(), "embedding chunks") {
		t.Fatalf("want the rejected batch's own error, located, got %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("a sibling's cancellation must not mask the real failure: %v", err)
	}
	assertNoCacheLeftBehind(t, cfg)
}

func TestBuild_cancellationStopsRetrying(t *testing.T) {
	withEmbedKnobs(t, 64)
	embedRetryDelay = func(int) time.Duration { return time.Hour }
	ctx, cancel := context.WithCancel(context.Background())
	st, cfg := harnessServing(t, embedInterceptor(&fakeAPI{}, func(w http.ResponseWriter, _ string, _ int32) bool {
		cancel() // the user gives up while the build is backing off
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	}))
	defer st.Close()

	errc := make(chan error, 1)
	go func() {
		_, err := Retrieve(ctx, st, cfg, Query{Text: "fire", Limit: 1})
		errc <- err
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want the cancellation returned, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation should interrupt the backoff sleep")
	}
	assertNoCacheLeftBehind(t, cfg)
}
