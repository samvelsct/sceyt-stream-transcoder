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

	registerScript *redis.Script
	fenceScript    *redis.Script
	deleteScript   *redis.Script
}

// New creates a Registry. ttl is the coarse safety-net expiry applied to
// every record (see RegistryConfig.SessionTTL) — a backstop against a
// permanent leak, not the primary cleanup path.
func New(rdb *redis.Client, keyPrefix string, ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Registry{
		rdb:            rdb,
		prefix:         keyPrefix,
		ttl:            ttl,
		registerScript: redis.NewScript(registerLuaScript),
		fenceScript:    redis.NewScript(fenceLuaScript),
		deleteScript:   redis.NewScript(deleteLuaScript),
	}
}

func (r *Registry) hashKey(sessionID string) string {
	return r.prefix + "session:" + sessionID
}

func (r *Registry) genKey(sessionID string) string {
	return r.prefix + "session:" + sessionID + ":gen"
}

// registerLuaScript atomically bumps the per-session generation counter and
// (re)writes the record under the new generation. Always succeeds — a fresh
// registration for a sessionID is, by definition, the new legitimate owner
// (see package docs on what generation fencing does and doesn't protect
// against).
const registerLuaScript = `
local gen = redis.call('INCR', KEYS[2])
redis.call('HSET', KEYS[1],
  'sessionId', ARGV[1],
  'workerId', ARGV[2],
  'origin', ARGV[3],
  'generation', gen,
  'status', ARGV[4],
  'leaseExpiresAt', ARGV[5])
redis.call('EXPIRE', KEYS[1], ARGV[6])
redis.call('EXPIRE', KEYS[2], ARGV[6])
return gen
`

// fenceLuaScript updates a single field only if the caller's generation
// still matches the record's current generation.
const fenceLuaScript = `
local cur = redis.call('HGET', KEYS[1], 'generation')
if not cur or cur ~= ARGV[1] then
  return 0
end
redis.call('HSET', KEYS[1], ARGV[2], ARGV[3])
if tonumber(ARGV[4]) > 0 then
  redis.call('EXPIRE', KEYS[1], ARGV[4])
end
return 1
`

// deleteLuaScript deletes a record only if the caller's generation still
// matches — mirrors the existing "only delete if this is still the same
// store" re-registration guard in httpserver.UnregisterSession.
const deleteLuaScript = `
local cur = redis.call('HGET', KEYS[1], 'generation')
if cur and cur == ARGV[1] then
  redis.call('DEL', KEYS[1])
  return 1
end
return 0
`

// Register creates (or re-creates) the ownership record for sessionID under
// workerID/origin, bumping the generation. Called once, at CreateSession
// success (internal/server/server.go's CreateSession handler).
func (r *Registry) Register(ctx context.Context, sessionID, workerID, origin string) (generation int64, err error) {
	now := time.Now()
	lease := now.Add(r.ttl)
	res, err := r.registerScript.Run(ctx, r.rdb,
		[]string{r.hashKey(sessionID), r.genKey(sessionID)},
		sessionID, workerID, origin, string(StatusActive),
		strconv.FormatInt(lease.UnixMilli(), 10),
		int(r.ttl.Seconds()),
	).Result()
	if err != nil {
		return 0, fmt.Errorf("registry: register %s: %w", sessionID, err)
	}
	gen, ok := res.(int64)
	if !ok {
		return 0, fmt.Errorf("registry: register %s: unexpected script result %T", sessionID, res)
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

func (r *Registry) setField(ctx context.Context, sessionID string, generation int64, field, value string, ttlSeconds int) error {
	res, err := r.fenceScript.Run(ctx, r.rdb,
		[]string{r.hashKey(sessionID)},
		strconv.FormatInt(generation, 10), field, value, ttlSeconds,
	).Result()
	if err != nil {
		return fmt.Errorf("registry: update %s: %w", sessionID, err)
	}
	if n, _ := res.(int64); n == 0 {
		return ErrNotOwner
	}
	return nil
}

// Delete removes a session's ownership record, fenced on generation so a
// late-firing grace-period timer from a superseded registration can never
// delete a newer owner's record. Called from the same grace-period
// time.AfterFunc that already clears the in-memory HLS store
// (internal/httpserver/server.go:594-617).
func (r *Registry) Delete(ctx context.Context, sessionID string, generation int64) error {
	res, err := r.deleteScript.Run(ctx, r.rdb,
		[]string{r.hashKey(sessionID)},
		strconv.FormatInt(generation, 10),
	).Result()
	if err != nil {
		return fmt.Errorf("registry: delete %s: %w", sessionID, err)
	}
	if n, _ := res.(int64); n == 0 {
		return ErrNotOwner
	}
	return nil
}

// Get resolves a session's current ownership record. Used by the Origin
// Router (Epic D1) on every viewer request not already served from its
// local short-TTL cache.
func (r *Registry) Get(ctx context.Context, sessionID string) (*Record, error) {
	vals, err := r.rdb.HGetAll(ctx, r.hashKey(sessionID)).Result()
	if err != nil {
		return nil, fmt.Errorf("registry: get %s: %w", sessionID, err)
	}
	if len(vals) == 0 {
		return nil, ErrNotFound
	}

	gen, err := strconv.ParseInt(vals["generation"], 10, 64)
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
		Generation:     gen,
		Status:         Status(vals["status"]),
		LeaseExpiresAt: lease,
	}, nil
}
