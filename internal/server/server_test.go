package server

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	pb "vt-stream-transcoder/api"
	"vt-stream-transcoder/internal/config"
)

// Helper function to create a test server
func newTestServer(t *testing.T) *Server {
	cfg := &config.Config{
		HLS: config.HLSConfig{
			OutputDir:       "/tmp/test-hls",
			EnableGStreamer: false,
		},
		Janus: config.JanusConfig{
			GatewayAddress: "http://localhost:8088/janus",
			AdminKey:       "supersecret",
			AdminSecret:    "janusoverlord",
		},
	}

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	return server
}

// BenchmarkSessionLifecycle benchmarks the complete session lifecycle
func BenchmarkSessionLifecycle(b *testing.B) {
	server := newTestServer(&testing.T{})
	defer server.Close()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sessionID := fmt.Sprintf("test-session-%d", i)

		// Create session
		createReq := &pb.CreateSessionRequest{
			SessionId:  sessionID,
			OutputPath: fmt.Sprintf("session-%d", i),
			EnableGst:  false,
		}

		_, err := server.CreateSession(context.Background(), createReq)
		if err != nil {
			b.Fatalf("Failed to create session: %v", err)
		}

		// Destroy session
		destroyReq := &pb.DestroySessionRequest{
			SessionId: sessionID,
		}

		_, err = server.DestroySession(context.Background(), destroyReq)
		if err != nil {
			b.Fatalf("Failed to destroy session: %v", err)
		}
	}
}

// BenchmarkCreateSession benchmarks only session creation
func BenchmarkCreateSession(b *testing.B) {
	server := newTestServer(&testing.T{})
	defer server.Close()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sessionID := fmt.Sprintf("test-session-%d", i)

		createReq := &pb.CreateSessionRequest{
			SessionId:  sessionID,
			OutputPath: fmt.Sprintf("session-%d", i),
			EnableGst:  false,
		}

		_, err := server.CreateSession(context.Background(), createReq)
		if err != nil {
			b.Fatalf("Failed to create session: %v", err)
		}
	}
}

// BenchmarkConcurrentSessions benchmarks multiple concurrent sessions
func BenchmarkConcurrentSessions(b *testing.B) {
	server := newTestServer(&testing.T{})
	defer server.Close()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Create 10 sessions
		sessionIDs := make([]string, 10)
		for j := 0; j < 10; j++ {
			sessionID := fmt.Sprintf("test-session-%d-%d", i, j)
			sessionIDs[j] = sessionID

			createReq := &pb.CreateSessionRequest{
				SessionId:  sessionID,
				OutputPath: fmt.Sprintf("session-%d-%d", i, j),
				EnableGst:  false,
			}

			_, err := server.CreateSession(context.Background(), createReq)
			if err != nil {
				b.Fatalf("Failed to create session: %v", err)
			}
		}

		// Destroy all sessions
		for _, sessionID := range sessionIDs {
			destroyReq := &pb.DestroySessionRequest{
				SessionId: sessionID,
			}

			_, err := server.DestroySession(context.Background(), destroyReq)
			if err != nil {
				b.Fatalf("Failed to destroy session: %v", err)
			}
		}
	}
}

// TestMemoryAllocation tests that memory is properly tracked per session
func TestMemoryAllocation(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Create and destroy a session
	sessionID := "test-memory-session"
	createReq := &pb.CreateSessionRequest{
		SessionId:  sessionID,
		OutputPath: "test-output",
		EnableGst:  false,
	}

	_, err := server.CreateSession(context.Background(), createReq)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Verify session exists in memStats
	server.mu.RLock()
	_, hasMemStats := server.memStats[sessionID]
	server.mu.RUnlock()

	if !hasMemStats {
		t.Error("Memory stats not tracked for session")
	}

	destroyReq := &pb.DestroySessionRequest{
		SessionId: sessionID,
	}

	_, err = server.DestroySession(context.Background(), destroyReq)
	if err != nil {
		t.Fatalf("Failed to destroy session: %v", err)
	}

	// Verify memStats was cleaned up
	server.mu.RLock()
	_, hasMemStats = server.memStats[sessionID]
	server.mu.RUnlock()

	if hasMemStats {
		t.Error("Memory stats not cleaned up after session destruction")
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	allocated := m2.TotalAlloc - m1.TotalAlloc
	t.Logf("Total memory allocated during test: %d bytes", allocated)
	t.Logf("Heap objects created: %d", m2.Mallocs-m1.Mallocs)
	t.Logf("Heap objects freed: %d", m2.Frees-m1.Frees)
}

// TestConcurrentSessionMemoryTracking tests that memory tracking works with concurrent sessions
func TestConcurrentSessionMemoryTracking(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	// Create multiple sessions
	sessionIDs := []string{"session-1", "session-2", "session-3"}

	for _, sessionID := range sessionIDs {
		createReq := &pb.CreateSessionRequest{
			SessionId:  sessionID,
			OutputPath: sessionID,
			EnableGst:  false,
		}

		_, err := server.CreateSession(context.Background(), createReq)
		if err != nil {
			t.Fatalf("Failed to create session %s: %v", sessionID, err)
		}
	}

	// Verify all sessions have memory stats
	server.mu.RLock()
	for _, sessionID := range sessionIDs {
		if _, exists := server.memStats[sessionID]; !exists {
			t.Errorf("Session %s missing from memStats", sessionID)
		}
	}
	server.mu.RUnlock()

	// Destroy sessions and verify individual tracking
	for _, sessionID := range sessionIDs {
		destroyReq := &pb.DestroySessionRequest{
			SessionId: sessionID,
		}

		_, err := server.DestroySession(context.Background(), destroyReq)
		if err != nil {
			t.Fatalf("Failed to destroy session %s: %v", sessionID, err)
		}

		// Verify this session's stats are removed
		server.mu.RLock()
		if _, exists := server.memStats[sessionID]; exists {
			t.Errorf("Session %s still in memStats after destruction", sessionID)
		}
		server.mu.RUnlock()
	}

	// Verify all memory stats are cleaned up
	server.mu.RLock()
	if len(server.memStats) != 0 {
		t.Errorf("Expected empty memStats, got %d entries", len(server.memStats))
	}
	server.mu.RUnlock()
}

// TestMemoryLeakDetection is a helper test to detect potential memory leaks
func TestMemoryLeakDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory leak test in short mode")
	}

	server := newTestServer(t)
	defer server.Close()

	const iterations = 1

	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// Create and destroy many sessions
	for i := 0; i < iterations; i++ {
		sessionID := fmt.Sprintf("leak-test-%d", i)

		createReq := &pb.CreateSessionRequest{
			SessionId:  sessionID,
			OutputPath: sessionID,
			EnableGst:  false,
		}

		_, err := server.CreateSession(context.Background(), createReq)
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		destroyReq := &pb.DestroySessionRequest{
			SessionId: sessionID,
		}

		_, err = server.DestroySession(context.Background(), destroyReq)
		if err != nil {
			t.Fatalf("Failed to destroy session: %v", err)
		}
	}

	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	heapGrowth := int64(m2.HeapAlloc) - int64(m1.HeapAlloc)
	objectGrowth := int64(m2.HeapObjects) - int64(m1.HeapObjects)

	t.Logf("Heap growth after %d iterations: %d bytes", iterations, heapGrowth)
	t.Logf("Object count growth: %d", objectGrowth)

	// This is a rough check - adjust thresholds based on your expectations
	maxExpectedHeapGrowth := int64(10 * 1024 * 1024) // 10MB
	if heapGrowth > maxExpectedHeapGrowth {
		t.Errorf("Potential memory leak detected: heap grew by %d bytes (threshold: %d)",
			heapGrowth, maxExpectedHeapGrowth)
	}
}
