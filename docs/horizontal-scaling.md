# Horizontal scaling

StreamBridge receives WebRTC from Janus and republishes LL-HLS. Historically it ran as a single instance —
all session state lives in that one process's memory (`internal/server/server.go`'s `sessions` map,
`internal/httpserver/server.go`'s `stores` map), capped at `Server.MaxConcurrentStreams`. This document
describes how it scales to N instances.

## Two separate problems, two separate owners

- **Placement** — which instance a *new* session should go to. Owned by **Fleet Controller**
  (`vt-fleet-controller`), the same system that already places Janus instances. StreamBridge is registered
  there as a `streambridge` fleet.
- **Viewer routing** — which instance already owns an *existing* session, for an anonymous viewer's LL-HLS
  GET request. Fleet Controller has no role here — it was never built to answer "who is serving this
  stream," only "who should get the next one." This is the **Ownership Registry + Origin Router**, owned
  entirely by this repo.

Nothing here builds a second placement mechanism. The instinct to add a load-tracking registry and a
least-loaded selector was the original plan, dropped once it became clear Fleet Controller already does
exactly that job for Janus and Coturn-shaped services.

## Placement: Fleet Controller

Signaling calls Fleet Controller's `SelectHost(service_name: "streambridge")` — a one-shot gRPC call, no
region hint, no session ID, no stickiness of any kind — and gets back a single instance address. It then
calls that instance's `CreateSession`/`AddInput`/etc. **directly**, remembering the address itself for the
rest of that session's life, exactly the pattern already used for Janus. There is no gRPC-proxying gateway
anywhere in this design — one was drafted early on and dropped once this became clear.

For Fleet Controller to place StreamBridge instances at all, two things had to exist:

1. **StreamBridge exposes `/metrics`** (`internal/httpserver/server.go`'s `metricsHandler`, same HTTP server
   as `/live/streams/...`) — a hand-written Prometheus exposition, not the full `client_golang` dependency,
   for three gauges:
   - `streambridge_up`
   - `streambridge_active_sessions`
   - `streambridge_max_concurrent_streams`

2. **Fleet Controller learned a `streambridge` worker type** — a new `internal/handlers/streambridge`
   package (Collector + Calculator) in the `vt-fleet-controller` repo, mirroring `internal/handlers/janus`
   structurally: it scrapes the metrics above plus a node-exporter sidecar, on the same two-source pattern
   Janus already uses (`application` + `node`).

**StreamBridge instances are Kubernetes-managed, not Fleet-Controller-provisioned** — a deliberate
divergence from Janus, which Fleet Controller provisions directly as Huawei ECS VMs. The `streambridge`
fleet runs in `lifecycle.mode: static` with `desiredCount: 0`: Fleet Controller discovers and monitors these
instances (via a `LifecycleController.List()` that watches the Kubernetes `streambridge` Service's
Endpoints) but never creates or destroys them — Kubernetes owns that entirely.

This combination — no `LifecycleController` registered, `desiredCount: 0` — didn't actually work when we
went to configure it: `ReconcileService` called `registry.Lifecycle(serviceName)` unconditionally and failed
every reconcile tick with `ErrHandlerNotRegistered`, regardless of `desiredCount`. Fixed in
`internal/core/reconciler.go`: a missing lifecycle controller is now a clean no-op specifically when
`desiredCount == 0` (nothing to converge toward), and still the original hard failure when `desiredCount >
0` (there'd be no way to create the missing instances). See
`TestReconcilerSucceedsWithoutLifecycleWhenDesiredCountIsZero` /
`TestReconcilerFailsWithoutLifecycleWhenDesiredCountIsPositive` in that repo.

## Viewer routing: Ownership Registry + Origin Router

### Ownership Registry (`internal/registry`)

Redis-backed. No `EVAL`/Lua scripting and no `WATCH`/`MULTI`/`EXEC` transactions — the Redis behind this may
be a twemproxy instance, which implements neither, so generation-fencing safety comes from the key scheme
itself instead of compare-and-swap:

- `{prefix}session:{sessionID}:gen` — a plain `INCR`-only counter, the current-generation pointer.
- `{prefix}session:{sessionID}:{generation}` — an **immutable per-generation** Redis hash: `workerId`,
  `origin` (host:port), `generation`, `status` (`ACTIVE`/`FINALIZING`), `leaseExpiresAt`. Each `Register`
  call mints a fresh generation via `INCR` and writes to that generation's own key — a key nothing else is
  ever concurrently writing to, so there's nothing to race. Readers always resolve the current generation
  from the counter first, then read exactly that one record key.

- **Written by the StreamBridge instance itself**, at `CreateSession` success
  (`internal/server/server.go`'s `CreateSession`, right after the session is stored in `s.sessions`) — no
  external allocator is involved in this step; the instance is registering ownership of a session it just
  created, on its own gRPC-driven `CreateSession` call.
- **Generation fencing.** Every fresh registration for a `sessionID` gets a new, strictly higher generation.
  `Finalize`/`Delete` write directly to the caller's own presented generation's key, never anyone else's —
  structurally impossible to collide, since a stale generation number always maps to a different Redis key
  than whatever's current. A stale caller is rejected with `ErrNotOwner` (checked via a plain, non-atomic
  read of the counter before the write — a tiny race can misreport that return value under extreme
  concurrent timing, but never lets a stale write reach the current generation's actual data). This is what
  stops a duplicate `CreateSession` call landing on two different instances, or a zombie process waking back
  up, from corrupting a session that's already moved on.
- **Cleared by the existing grace-period timer**, not a new one:
  `internal/httpserver/server.go`'s `UnregisterSession` already keeps a destroyed session's HLS store around
  for `unregisterGracePeriod` (10s) so in-flight playlist polls still see a final `#EXT-X-ENDLIST` instead of
  a bare 404. The registry delete now happens inside that same `time.AfterFunc`, fenced on the same
  generation, so a session recreated under the same ID during the grace window is never touched by the stale
  timer.
- **`SessionTTL`** (default 24h) is a coarse safety-net expiry on every record — a backstop against a
  permanent leak if an instance crashes mid-session and the grace-period path never runs, not the primary
  cleanup mechanism.

### Origin Router (`internal/originrouter`, binary: `cmd/streambridge-origin-router`)

A stateless HTTP reverse proxy, no CGo/native-library dependency, that viewers actually hit at
`/live/streams/{sessionID}/...` — the one public entry point for LL-HLS traffic.

1. Parses `sessionID` from the path — identically to `internal/httpserver.Server.sessionRouter`'s own
   parsing, so the two sides of the proxy never disagree about path structure.
2. Resolves ownership from the registry, cached locally for `OwnershipCacheTTL` (2s default) to avoid a
   Redis round trip on every single request.
3. **Liveness cross-check**: calls Fleet Controller's `GetInstance(service_name: "streambridge",
   instance_id: workerID)` (`internal/fleetclient`) before proxying. If Fleet Controller reports the
   instance `UNHEALTHY` or doesn't know about it at all, the session is treated as orphaned — a clean 404,
   not a hang against a dead address. This reuses Fleet Controller's own health tracking instead of
   duplicating it as a second liveness registry. Optional: with `FleetController.Enabled: false`, this
   degrades to a no-op (`fleetclient.NoopChecker`) and the router still functions, just without the
   cross-check.
4. Proxies via `httputil.ReverseProxy`, configured so LL-HLS's blocking-playlist reload (which can hold a
   response open for up to `3 × SegmentDuration` seconds, see `servePlaylist`/`servePart` in
   `internal/httpserver/server.go`) is never cut short: `ResponseHeaderTimeout: 0`, no response buffering,
   and every query parameter (`_HLS_msn`, `_HLS_part`) and header (`Range`, conditional, auth) preserved
   verbatim.
5. **Never falls back to a different worker.** A proxy failure evicts the cache entry (so the *next* request
   re-resolves fresh) and returns a clean error — it does not guess at another instance, because a
   different instance never had that session's data to begin with.

`internal/fleetclient`'s generated stub (`api/fleetpb`, from `proto/fleetcontroller.proto`, regenerated via
`make fleetproto`) is a **local copy** of Fleet Controller's own `FleetStatusService` contract — Fleet
Controller's generated Go code lives under its own `internal/` package and can't be imported cross-module,
so this repo generates its own client from the same wire contract instead. Re-run `make fleetproto` after
copying over any upstream proto changes.

## What happens when an instance crashes

| Failure | Behavior |
|---|---|
| Instance process crash | All in-memory state on that instance is gone — Janus input dies with the process, segment buffers vanish. True today too (single-instance); scaling only shrinks the blast radius. |
| Existing viewers of its sessions | Get a clean 404 via the Origin Router's liveness cross-check (once Fleet Controller marks the instance unhealthy/gone) rather than a graceful `#EXT-X-ENDLIST` — the crash never runs the normal grace-period shutdown path. No migration is possible; the in-memory data is simply gone. |
| New `CreateSession` calls | Unaffected once Fleet Controller's metrics collection marks the instance unhealthy/stale — `SelectHost` stops returning it. |
| Stale registry record for a crashed instance's session | Self-heals via the Origin Router's per-request liveness cross-check (no separate reconciliation sweep needed) — orphaned as soon as Fleet Controller reports the owner unhealthy, backstopped by `SessionTTL` regardless. |

**Non-goal, stated plainly**: no transparent live migration of encoder/compositor state on crash. Fixing
that would require stream-level redundancy (dual-publishing a stream to two instances simultaneously), which
is real added complexity and out of scope here.

## Deployment

`deploy/k8s/`: `streamer.yaml` (StreamBridge Deployment + a Service used *only* for Fleet Controller's
Endpoints-based discovery, never for actual session calls — see the warning comment in that file),
`origin-router.yaml` (stateless Deployment + the Service viewers actually hit), `redis.yaml` (single
instance, explicitly not HA for this phase).

**Open risk, unresolved**: whether `webrtchls`'s ICE/DTLS negotiation with Janus survives ordinary
Kubernetes pod networking, or needs `hostNetwork: true` the way LiveKit's WebRTC SFU does for the same class
of NAT-related reason. This needs a real test against the target cluster — it's the one thing in this design
that could force a manifest change after the fact.

## Resource sizing

Size StreamBridge pods by resource class (Small/Medium/Large/GPU), not one fixed number for every session —
a session's cost scales with participant count, source codec, resolution, and output profile. Set CPU
**request** ≈ that class's sustainable load, **limit** = burst headroom: Kubernetes schedules by request, so
a too-low request with a high limit lets the scheduler pack more pods onto a node than can actually run
simultaneously at their limit, causing real CPU throttling and visible mux latency under load. `deploy/k8s/streamer.yaml`'s current request/limit values are Medium-class placeholders — benchmark before trusting them
in production.

**No HPA directly on StreamBridge pods.** An HPA can't tell which pod is mid-session and safe to terminate.
Capacity/instance-count decisions for a Kubernetes-managed-but-Fleet-Controller-known fleet like this belong
with whatever already makes that call for Janus (Fleet Controller's own autoscaler, or the Kubernetes
Deployment's replica count set deliberately) — not a second, competing autoscaling authority.
