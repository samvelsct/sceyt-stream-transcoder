package server

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	pb "vt-stream-transcoder/api"
	"vt-stream-transcoder/internal/registry"
)

func newTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return registry.New(rdb, "streambridge:", 0)
}

// CreateSession must publish an Ownership Registry record (Epic C2) on
// success, and DestroySession must mark it FINALIZING — the seam the Origin
// Router and the httpserver grace-period cleanup both depend on.
func TestCreateSession_PublishesOwnershipRecord(t *testing.T) {
	reg := newTestRegistry(t)
	server := newTestServer(t)
	defer server.Close()
	server.SetSessionRegistry(reg, "pod-a", "10.0.0.5:8080")

	const sessionID = "sess-registry-1"
	_, err := server.CreateSession(context.Background(), &pb.CreateSessionRequest{
		SessionId:  sessionID,
		OutputPath: sessionID,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	rec, err := reg.Get(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("expected an ownership record to exist, got error: %v", err)
	}
	if rec.WorkerID != "pod-a" || rec.Origin != "10.0.0.5:8080" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if rec.Status != registry.StatusActive {
		t.Fatalf("expected status ACTIVE right after CreateSession, got %q", rec.Status)
	}

	server.mu.RLock()
	gen, ok := server.generations[sessionID]
	server.mu.RUnlock()
	if !ok || gen != rec.Generation {
		t.Fatalf("expected server.generations[%s]=%d to match the record's generation %d", sessionID, gen, rec.Generation)
	}
}

func TestDestroySession_FinalizesOwnershipRecordAndForgetsGeneration(t *testing.T) {
	reg := newTestRegistry(t)
	server := newTestServer(t)
	defer server.Close()
	server.SetSessionRegistry(reg, "pod-a", "10.0.0.5:8080")

	const sessionID = "sess-registry-2"
	ctx := context.Background()
	if _, err := server.CreateSession(ctx, &pb.CreateSessionRequest{SessionId: sessionID, OutputPath: sessionID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := server.DestroySession(ctx, &pb.DestroySessionRequest{SessionId: sessionID}); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}

	rec, err := reg.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("expected the record to still exist (grace-period delete is httpserver's job), got: %v", err)
	}
	if rec.Status != registry.StatusFinalizing {
		t.Fatalf("expected status FINALIZING after DestroySession, got %q", rec.Status)
	}

	server.mu.RLock()
	_, ok := server.generations[sessionID]
	server.mu.RUnlock()
	if ok {
		t.Fatalf("expected server.generations to forget %s after DestroySession", sessionID)
	}
}

// With no registry configured (the default), session lifecycle must behave
// exactly as before the registry existed — no panics, no attempted writes.
func TestSessionLifecycle_WithoutRegistry_Unaffected(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	const sessionID = "sess-no-registry"
	ctx := context.Background()
	if _, err := server.CreateSession(ctx, &pb.CreateSessionRequest{SessionId: sessionID, OutputPath: sessionID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := server.DestroySession(ctx, &pb.DestroySessionRequest{SessionId: sessionID}); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}
}
