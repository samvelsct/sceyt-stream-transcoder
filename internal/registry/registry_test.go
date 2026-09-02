package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
)

func newTestRegistry(t *testing.T, ttl time.Duration) *Registry {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return New(rdb, "streambridge:", ttl)
}

func TestRegister_CreatesRecordAtGenerationOne(t *testing.T) {
	r := newTestRegistry(t, time.Minute)
	ctx := context.Background()

	gen, err := r.Register(ctx, "sess-1", "pod-a", "10.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if gen != 1 {
		t.Fatalf("expected generation 1, got %d", gen)
	}

	rec, err := r.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.WorkerID != "pod-a" || rec.Origin != "10.0.0.1:8080" || rec.Generation != 1 || rec.Status != StatusActive {
		t.Fatalf("unexpected record: %+v", rec)
	}
}

func TestRegister_ReRegistrationBumpsGeneration(t *testing.T) {
	r := newTestRegistry(t, time.Minute)
	ctx := context.Background()

	gen1, err := r.Register(ctx, "sess-1", "pod-a", "10.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register #1: %v", err)
	}
	gen2, err := r.Register(ctx, "sess-1", "pod-b", "10.0.0.2:8080")
	if err != nil {
		t.Fatalf("Register #2: %v", err)
	}
	if gen2 <= gen1 {
		t.Fatalf("expected generation to increase, got %d then %d", gen1, gen2)
	}

	rec, err := r.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.WorkerID != "pod-b" {
		t.Fatalf("expected the second registration to be the current owner, got %+v", rec)
	}
}

func TestFinalize_RejectsStaleGeneration(t *testing.T) {
	r := newTestRegistry(t, time.Minute)
	ctx := context.Background()

	staleGen, err := r.Register(ctx, "sess-1", "pod-a", "10.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// A second registration (e.g. a duplicate CreateSession landing on a
	// different instance) supersedes pod-a's generation.
	if _, err := r.Register(ctx, "sess-1", "pod-b", "10.0.0.2:8080"); err != nil {
		t.Fatalf("Register #2: %v", err)
	}

	// pod-a, unaware it's been superseded, tries to finalize under its now-stale generation.
	if err := r.Finalize(ctx, "sess-1", staleGen); err != ErrNotOwner {
		t.Fatalf("expected ErrNotOwner for a stale generation, got %v", err)
	}

	rec, err := r.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != StatusActive {
		t.Fatalf("pod-a's stale Finalize must not have affected pod-b's record, got status %q", rec.Status)
	}
}

func TestFinalize_SucceedsUnderCurrentGeneration(t *testing.T) {
	r := newTestRegistry(t, time.Minute)
	ctx := context.Background()

	gen, err := r.Register(ctx, "sess-1", "pod-a", "10.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Finalize(ctx, "sess-1", gen); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	rec, err := r.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != StatusFinalizing {
		t.Fatalf("expected status FINALIZING, got %q", rec.Status)
	}
}

func TestDelete_RejectsStaleGeneration_MirrorsGracePeriodGuard(t *testing.T) {
	r := newTestRegistry(t, time.Minute)
	ctx := context.Background()

	staleGen, err := r.Register(ctx, "sess-1", "pod-a", "10.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// A new session gets created under the same ID before the old grace-period
	// timer fires (the exact scenario httpserver.UnregisterSession's
	// `s.stores[sessionID] == st` check guards against for the in-memory store).
	newGen, err := r.Register(ctx, "sess-1", "pod-b", "10.0.0.2:8080")
	if err != nil {
		t.Fatalf("Register #2: %v", err)
	}

	if err := r.Delete(ctx, "sess-1", staleGen); err != ErrNotOwner {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}
	rec, err := r.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("record should still exist after a stale-generation delete attempt: %v", err)
	}
	if rec.Generation != newGen {
		t.Fatalf("expected the surviving record to be pod-b's, got %+v", rec)
	}

	if err := r.Delete(ctx, "sess-1", newGen); err != nil {
		t.Fatalf("Delete under the current generation should succeed: %v", err)
	}
	if _, err := r.Get(ctx, "sess-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestRefresh_ExtendsLeaseUnderCurrentGeneration(t *testing.T) {
	r := newTestRegistry(t, time.Minute)
	ctx := context.Background()

	gen, err := r.Register(ctx, "sess-1", "pod-a", "10.0.0.1:8080")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	before, err := r.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	if err := r.Refresh(ctx, "sess-1", gen); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	after, err := r.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !after.LeaseExpiresAt.After(before.LeaseExpiresAt) {
		t.Fatalf("expected lease to be extended: before=%v after=%v", before.LeaseExpiresAt, after.LeaseExpiresAt)
	}
}

func TestGet_UnknownSessionReturnsErrNotFound(t *testing.T) {
	r := newTestRegistry(t, time.Minute)
	if _, err := r.Get(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRegister_ExpiresAfterTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	r := New(rdb, "streambridge:", 10*time.Second)

	if _, err := r.Register(context.Background(), "sess-1", "pod-a", "10.0.0.1:8080"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	mr.FastForward(11 * time.Second)

	if _, err := r.Get(context.Background(), "sess-1"); err != ErrNotFound {
		t.Fatalf("expected the coarse safety-net TTL to expire the record, got %v", err)
	}
}

func TestWithRetry_SucceedsWithoutRetryOnNilError(t *testing.T) {
	calls := 0
	err := withRetry(func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("withRetry: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call, got %d", calls)
	}
}

func TestWithRetry_RetriesTransientErrorsThenSucceeds(t *testing.T) {
	calls := 0
	err := withRetry(func() error {
		calls++
		if calls < retryAttempts {
			return errors.New("simulated EOF")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRetry: %v", err)
	}
	if calls != retryAttempts {
		t.Fatalf("expected %d calls (fails until the last attempt), got %d", retryAttempts, calls)
	}
}

func TestWithRetry_GivesUpAfterRetryAttemptsAndReturnsLastError(t *testing.T) {
	calls := 0
	sentinel := errors.New("simulated persistent EOF")
	err := withRetry(func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the last error to propagate, got %v", err)
	}
	if calls != retryAttempts {
		t.Fatalf("expected exactly %d attempts, got %d", retryAttempts, calls)
	}
}

func TestWithRetry_DoesNotRetryErrNotFoundOrErrNotOwner(t *testing.T) {
	for _, sentinel := range []error{ErrNotFound, ErrNotOwner} {
		calls := 0
		err := withRetry(func() error {
			calls++
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected %v to propagate, got %v", sentinel, err)
		}
		if calls != 1 {
			t.Fatalf("expected %v to short-circuit after 1 call (not a transient failure), got %d calls", sentinel, calls)
		}
	}
}
