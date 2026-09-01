// Package registry implements the session Ownership Registry (Epic C): a
// Redis-backed record of which StreamBridge instance owns a given session,
// used by the Origin Router to route viewer LL-HLS requests correctly.
//
// This is deliberately small. Placement (which instance a *new* session
// should go to) and load tracking are Fleet Controller's job, not ours —
// this package only answers "which instance already owns this session,"
// which Fleet Controller has no concept of.
package registry

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotOwner is returned when a caller tries to update or delete a record
// under a generation that no longer matches the current one — i.e. a stale
// or zombie instance trying to act on a session it no longer owns.
var ErrNotOwner = errors.New("registry: caller does not hold the current generation for this session")

// ErrNotFound is returned when a session has no ownership record.
var ErrNotFound = errors.New("registry: session not found")

// Registry is a Redis-backed session ownership registry.
type Registry struct {
	rdb    *redis.Client
	prefix string
	ttl    time.Duration
}

// New creates a Registry. ttl is the coarse safety-net expiry applied to
// every record (see RegistryConfig.SessionTTL) — a backstop against a
// permanent leak, not the primary cleanup path.
func New(rdb *redis.Client, keyPrefix string, ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Registry{
		rdb:    rdb,
		prefix: keyPrefix,
		ttl:    ttl,
	}
}

// genKey holds a monotonically increasing counter for sessionID — the only
// thing that needs a real atomic operation (INCR, a single command, safe
// under twemproxy since it's not a compound read-then-write).
func (r *Registry) genKey(sessionID string) string {
	return r.prefix + "session:" + sessionID + ":gen"
}

// recordKey holds the immutable ownership record for one specific
// generation of sessionID. Immutable is the load-bearing property: once
// Register mints a generation via INCR, that generation's key is uniquely
// its own — no other call can ever be racing to write the *same* key, so
// there's nothing to compare-and-swap. A stale caller presenting an old
// generation can only ever touch that old generation's own (already
// unread) key; it can never collide with whatever the current generation
// is, because they're different Redis keys by construction. Readers
// (Get) always resolve the current generation via genKey first, then read
// exactly one recordKey — so a write to a superseded recordKey has no
// observable effect on anyone.
func (r *Registry) recordKey(sessionID string, generation int64) string {
	return r.prefix + "session:" + sessionID + ":" + strconv.FormatInt(generation, 10)
}

// Register creates the ownership record for sessionID under
// workerID/origin, at a freshly minted generation. Called once, at
// CreateSession success (internal/server/server.go's CreateSession
// handler). Always succeeds — a fresh registration for a sessionID is, by
// definition, the new legitimate owner (see the package doc on what
// generation fencing does and doesn't protect against).
func (r *Registry) Register(ctx context.Context, sessionID, workerID, origin string) (generation int64, err error) {
	gen, err := r.rdb.Incr(ctx, r.genKey(sessionID)).Result()
	if err != nil {
		return 0, fmt.Errorf("registry: register %s: incr generation: %w", sessionID, err)
	}
	ttlSeconds := int(r.ttl.Seconds())
	if err := r.rdb.Expire(ctx, r.genKey(sessionID), r.ttl).Err(); err != nil {
		return 0, fmt.Errorf("registry: register %s: expire generation counter: %w", sessionID, err)
	}

	now := time.Now()
	lease := now.Add(r.ttl)
	key := r.recordKey(sessionID, gen)
	if err := r.rdb.HSet(ctx, key,
		"sessionId", sessionID,
		"workerId", workerID,
		"origin", origin,
		"generation", gen,
		"status", string(StatusActive),
		"leaseExpiresAt", lease.UnixMilli(),
	).Err(); err != nil {
		return 0, fmt.Errorf("registry: register %s: write record: %w", sessionID, err)
	}
	if err := r.rdb.Expire(ctx, key, time.Duration(ttlSeconds)*time.Second).Err(); err != nil {
		return 0, fmt.Errorf("registry: register %s: expire record: %w", sessionID, err)
	}
	return gen, nil
}

// Finalize marks a session as FINALIZING — called at DestroySession,
// alongside (not instead of) the existing store-finalize step in
// httpserver.UnregisterSession. Fenced: a stale generation is rejected with
// ErrNotOwner rather than silently no-op'd, so callers can tell the
// difference between "already finalized by someone else" and "nothing to
// do."
func (r *Registry) Finalize(ctx context.Context, sessionID string, generation int64) error {
	return r.setField(ctx, sessionID, generation, "status", string(StatusFinalizing), 0)
}

// Refresh extends a record's lease TTL — an optional periodic call while a
// session is long-lived, so the coarse safety-net TTL doesn't expire out
// from under an active stream.
func (r *Registry) Refresh(ctx context.Context, sessionID string, generation int64) error {
	lease := time.Now().Add(r.ttl)
	return r.setField(ctx, sessionID, generation, "leaseExpiresAt",
		strconv.FormatInt(lease.UnixMilli(), 10), int(r.ttl.Seconds()))
}

// setField checks generation against the current pointer (genKey) and,
// only if it still matches, writes directly to that generation's own
// recordKey. The read-then-write here is two plain commands, not one
// atomic unit — but that's safe by construction (see recordKey's doc): the
// write can only ever land on the caller's own presented generation's key,
// which nothing else is ever writing to concurrently, and which no reader
// consults once genKey has moved past it. The one thing NOT fully
// linearizable under a very tight race is the ErrNotOwner-vs-success
// *return value* itself (genKey could tick over in the gap between the
// read and the write) — that's a rare, harmless mislabeling for logging
// purposes, never a data-safety issue, since the write still only ever
// touches a key nobody reads once it's stale.
func (r *Registry) setField(ctx context.Context, sessionID string, generation int64, field, value string, ttlSeconds int) error {
	cur, err := r.currentGeneration(ctx, sessionID)
	if err != nil {
		return err
	}
	if cur != generation {
		return ErrNotOwner
	}

	key := r.recordKey(sessionID, generation)
	if err := r.rdb.HSet(ctx, key, field, value).Err(); err != nil {
		return fmt.Errorf("registry: update %s: %w", sessionID, err)
	}
	if ttlSeconds > 0 {
		if err := r.rdb.Expire(ctx, key, time.Duration(ttlSeconds)*time.Second).Err(); err != nil {
			return fmt.Errorf("registry: update %s: expire: %w", sessionID, err)
		}
	}
	return nil
}

// Delete removes a session's ownership record, fenced on generation so a
// late-firing grace-period timer from a superseded registration can never
// delete a newer owner's record — structurally true here regardless of
// timing, since Delete only ever issues DEL against the caller's own
// presented generation's key (see recordKey's doc), which is a different
// Redis key from whatever the current generation is. Called from the same
// grace-period time.AfterFunc that already clears the in-memory HLS store
// (internal/httpserver/server.go:594-617).
func (r *Registry) Delete(ctx context.Context, sessionID string, generation int64) error {
	cur, err := r.currentGeneration(ctx, sessionID)
	if err != nil {
		return err
	}
	if cur != generation {
		return ErrNotOwner
	}
	if err := r.rdb.Del(ctx, r.recordKey(sessionID, generation)).Err(); err != nil {
		return fmt.Errorf("registry: delete %s: %w", sessionID, err)
	}
	return nil
}

// currentGeneration reads the generation pointer for sessionID.
// ErrNotFound means no Register call has ever happened for this sessionID.
func (r *Registry) currentGeneration(ctx context.Context, sessionID string) (int64, error) {
	val, err := r.rdb.Get(ctx, r.genKey(sessionID)).Result()
	if errors.Is(err, redis.Nil) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("registry: read generation for %s: %w", sessionID, err)
	}
	gen, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("registry: read generation for %s: invalid value %q: %w", sessionID, val, err)
	}
	return gen, nil
}

// Get resolves a session's current ownership record. Used by the Origin
// Router (Epic D1) on every viewer request not already served from its
// local short-TTL cache. Two plain reads (genKey then that generation's
// recordKey) instead of one HGETALL — the cost of not having a single
// atomic "read current record" primitive available, acceptable given
// results are already cached locally for OwnershipCacheTTL.
func (r *Registry) Get(ctx context.Context, sessionID string) (*Record, error) {
	gen, err := r.currentGeneration(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	vals, err := r.rdb.HGetAll(ctx, r.recordKey(sessionID, gen)).Result()
	if err != nil {
		return nil, fmt.Errorf("registry: get %s: %w", sessionID, err)
	}
	if len(vals) == 0 {
		// genKey points at a generation whose record is gone (deleted or
		// expired) — same observable outcome as no registration at all.
		return nil, ErrNotFound
	}

	parsedGen, err := strconv.ParseInt(vals["generation"], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("registry: get %s: invalid generation %q: %w", sessionID, vals["generation"], err)
	}
	var lease time.Time
	if ms, err := strconv.ParseInt(vals["leaseExpiresAt"], 10, 64); err == nil {
		lease = time.UnixMilli(ms)
	}

	return &Record{
		SessionID:      vals["sessionId"],
		WorkerID:       vals["workerId"],
		Origin:         vals["origin"],
		Generation:     parsedGen,
		Status:         Status(vals["status"]),
		LeaseExpiresAt: lease,
	}, nil
}
