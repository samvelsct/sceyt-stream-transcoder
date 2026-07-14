package server

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"vt-stream-transcoder/internal/webrtchls"

	pb "vt-stream-transcoder/api"
	"vt-stream-transcoder/internal/config"
	"vt-stream-transcoder/internal/httpserver"

	zlog "github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the StreamBridge gRPC service
type Server struct {
	pb.UnimplementedStreamBridgeServer
	ctx      *webrtchls.Context
	sessions map[string]*webrtchls.Session
	config   *config.Config
	hlsSrv   *httpserver.Server // LL-HLS HTTP server (may be nil)
	mu       sync.RWMutex
	memStats map[string]runtime.MemStats // Per-session memory tracking
}

// NewServer creates a new StreamBridge server.
// hlsSrv may be nil; when provided, each session will be registered with the
// HTTP server so its segments are served at /streams/{sessionID}/...
func NewServer(cfg *config.Config, hlsSrv *httpserver.Server) (serverPtr *Server, finalErr error) {
	// Add panic recovery with detailed logging
	defer func() {
		if r := recover(); r != nil {
			zlog.Error().Msgf("!!! PANIC in NewServer: %v !!!", r)
			zlog.Error().Msg("=== PANIC STACK TRACE ===")
			zlog.Error().Msg(string(debug.Stack()))
			zlog.Error().Msg("=== END PANIC STACK TRACE ===")

			finalErr = fmt.Errorf("panic in NewServer: %v", r)
			serverPtr = nil
		}
	}()

	zlog.Info().Msg("Initializing webrtchls library...")

	// Initialize the webrtchls library
	if err := webrtchls.Init(); err != nil {
		zlog.Error().Err(err).Msg("Failed to initialize webrtchls")
		return nil, fmt.Errorf("failed to initialize webrtchls: %w", err)
	}
	zlog.Info().Msg("webrtchls library initialized successfully")

	// Create a context
	zlog.Info().Msg("Creating webrtchls context...")
	ctx := webrtchls.NewContext()
	if ctx == nil {
		zlog.Error().Msg("webrtchls.NewContext() returned nil")
		webrtchls.Cleanup()
		return nil, fmt.Errorf("failed to create webrtchls context")
	}
	zlog.Info().Msg("webrtchls context created successfully")

	zlog.Info().Msg("Creating Server struct...")
	server := &Server{
		ctx:      ctx,
		sessions: make(map[string]*webrtchls.Session),
		config:   cfg,
		hlsSrv:   hlsSrv,
		memStats: make(map[string]runtime.MemStats),
	}
	zlog.Info().Msg("Server struct created successfully")

	return server, nil
}

// Close cleans up the server resources
func (s *Server) Close() {
	zlog.Info().Msgf("[MUTEX] Close write lock")
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		zlog.Info().Msgf("[MUTEX] Close write unlock")
	}()

	// Destroy all sessions
	for _, session := range s.sessions {
		session.Destroy()
	}
	s.sessions = nil

	// Destroy context
	if s.ctx != nil {
		s.ctx.Destroy()
		s.ctx = nil
	}

	// Cleanup library
	webrtchls.Cleanup()
}

// CreateSession creates a new HLS output session
func (s *Server) CreateSession(_ context.Context, req *pb.CreateSessionRequest) (*pb.CreateSessionResponse, error) {
	zlog.Info().Msgf("[%s] CreateSessionRequest: %v", req.SessionId, req)
	if req.SessionId == "" {
		return &pb.CreateSessionResponse{
			Success: false,
			Message: "session_id is required",
		}, status.Error(codes.InvalidArgument, "session_id is required")
	}

	if req.OutputPath == "" {
		return &pb.CreateSessionResponse{
			Success: false,
			Message: "output_path is required",
		}, status.Error(codes.InvalidArgument, "output_path is required")
	}

	zlog.Info().Msgf("[%s][MUTEX] CreateSession: write lock", req.SessionId)
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		zlog.Info().Msgf("[%s][MUTEX] CreateSession: write unlocked", req.SessionId)
	}()

	// Check if session already exists
	if _, exists := s.sessions[req.SessionId]; exists {
		return &pb.CreateSessionResponse{
			Success: false,
			Message: "session already exists",
		}, status.Error(codes.AlreadyExists, "session already exists")
	}

	// Use configured output directory if path is relative
	outputPath := req.OutputPath
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(s.config.HLS.OutputDir, outputPath)
	}

	zlog.Info().Msgf("[%s] CreateSession: output_path=%s", req.SessionId, outputPath)

	// Use configured GStreamer setting if not specified in request
	enableGst := req.EnableGst
	if !req.EnableGst && s.config.HLS.EnableGStreamer {
		enableGst = true
	}

	// Create the session
	zlog.Info().Msgf("[%s] CreateSession: calling ctx.CreateSession (CGo, under lock)", req.SessionId)
	session, err := s.ctx.CreateSession(&webrtchls.SessionConfig{
		SessionID:          req.SessionId,
		OutputPath:         outputPath,
		EnableGst:          enableGst,
		VideoWidth:         s.config.HLS.Width,
		VideoHeight:        s.config.HLS.Height,
		VideoFPS:           s.config.HLS.FPS,
		PartDurationSec:    s.config.HLS.PartDuration,
		SegmentDurationSec: s.config.HLS.SegmentDuration,
	})
	zlog.Info().Msgf("[%s] CreateSession: ctx.CreateSession returned (err=%v)", req.SessionId, err)
	if err != nil {
		return &pb.CreateSessionResponse{
			Success: false,
			Message: fmt.Sprintf("failed to create session: %v", err),
		}, status.Error(codes.Internal, err.Error())
	}

	s.sessions[req.SessionId] = session

	// Register with the LL-HLS HTTP server so segments are served over HTTP.
	if s.hlsSrv != nil {
		zlog.Info().Msgf("[%s] CreateSession: calling hlsSrv.RegisterSession (under lock)", req.SessionId)
		if regErr := s.hlsSrv.RegisterSession(req.SessionId, session); regErr != nil {
			zlog.Warn().Msgf("[%s] Failed to register session with HLS HTTP server %v", req.SessionId, regErr)
		}
		zlog.Info().Msgf("[%s] CreateSession: hlsSrv.RegisterSession returned", req.SessionId)
	}

	// Capture memory stats after session creation (STW pause under lock)
	zlog.Info().Msgf("[%s] CreateSession: calling runtime.ReadMemStats (STW, under lock)", req.SessionId)
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	s.memStats[req.SessionId] = memStats
	zlog.Info().Msgf("[%s] CreateSession: runtime.ReadMemStats returned", req.SessionId)

	return &pb.CreateSessionResponse{
		Success: true,
		Message: "session created successfully",
	}, nil
}

// DestroySession destroys an existing session
func (s *Server) DestroySession(_ context.Context, req *pb.DestroySessionRequest) (*pb.DestroySessionResponse, error) {
	zlog.Info().Msgf("[%s] DestroySession: %v", req.SessionId, req)

	zlog.Info().Msgf("[%s][MUTEX] DestroySession: write lock", req.SessionId)
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		zlog.Info().Msgf("[%s][MUTEX] DestroySession: write unlocked", req.SessionId)
	}()

	session, exists := s.sessions[req.SessionId]
	if !exists {
		return &pb.DestroySessionResponse{
			Success: false,
			Message: "session not found",
		}, status.Error(codes.NotFound, "session not found")
	}

	zlog.Info().Msgf("[%s] DestroySession: session.Destroy", req.SessionId)
	session.Destroy()
	delete(s.sessions, req.SessionId)

	// Unregister from the LL-HLS HTTP server.
	if s.hlsSrv != nil {
		zlog.Info().Msgf("[%s] DestroySession: s.hlsSrv.UnregisterSession", req.SessionId)
		s.hlsSrv.UnregisterSession(req.SessionId)
	}

	// Get memory stats for this specific session
	m1, hasStats := s.memStats[req.SessionId]
	delete(s.memStats, req.SessionId)

	if hasStats {
		// Force GC to get more accurate measurements
		runtime.GC()

		var m2 runtime.MemStats
		runtime.ReadMemStats(&m2)

		allocated := m2.TotalAlloc - m1.TotalAlloc
		heapDelta := int64(m2.HeapAlloc) - int64(m1.HeapAlloc)
		mallocsDelta := m2.Mallocs - m1.Mallocs
		freesDelta := m2.Frees - m1.Frees

		zlog.Info().Msgf("[%s] Session memory statistics:\n\ttotal_allocated_bytes=%d\n\theap_delta_bytes=%d\n\tmallocs=%d\n\tfrees=%d\n\tlive_objects=%d", req.SessionId, allocated, heapDelta, mallocsDelta, freesDelta, mallocsDelta-freesDelta)
	}

	zlog.Info().Msgf("[%s] DestroySession: exit", req.SessionId)
	return &pb.DestroySessionResponse{
		Success: true,
		Message: "session destroyed successfully",
	}, nil
}

// AddInput adds a WebRTC input to a session
func (s *Server) AddInput(_ context.Context, req *pb.AddInputRequest) (*pb.AddInputResponse, error) {
	zlog.Info().Msgf("[%s] AddInput: %v", req.SessionId, req)

	zlog.Info().Msgf("[%s][MUTEX] AddInput read lock", req.SessionId)
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()
	zlog.Info().Msgf("[%s][MUTEX] AddInput read unlock", req.SessionId)

	if !exists {
		zlog.Warn().Msgf("[%s] AddInput: session not found", req.SessionId)
		return &pb.AddInputResponse{
			Success: false,
			Message: "session not found",
		}, status.Error(codes.NotFound, "session not found")
	}

	// Use configured defaults if not provided in request
	janusGateway := req.JanusGatewayAddress
	if janusGateway == "" {
		janusGateway = s.config.Janus.GatewayAddress
	}

	janusAdminKey := req.JanusAdminKey
	if janusAdminKey == "" {
		janusAdminKey = s.config.Janus.AdminKey
	}

	janusAdminSecret := req.JanusAdminSecret
	if janusAdminSecret == "" {
		janusAdminSecret = s.config.Janus.AdminSecret
	}

	inputConfig := &webrtchls.InputConfig{
		JanusRoomID:         req.JanusRoomId,
		JanusSessionID:      req.JanusSessionId,
		JanusHandleID:       req.JanusHandleId,
		JanusPublisherID:    req.JanusPublisherId,
		JanusGatewayAddress: janusGateway,
		JanusAdminKey:       janusAdminKey,
		JanusAdminSecret:    janusAdminSecret,
		DisplayName:         req.DisplayName,
	}

	zlog.Info().Msgf("[%s] Calling session.AddInput with config %v", req.SessionId, req)

	err := session.AddInput(inputConfig)
	if err != nil {
		zlog.Error().Msgf("[%s] AddInput: Failed to add input", req.SessionId)
		return &pb.AddInputResponse{
			Success: false,
			Message: fmt.Sprintf("failed to add input: %v", err),
		}, status.Error(codes.Internal, err.Error())
	}

	return &pb.AddInputResponse{
		Success: true,
		Message: "input added successfully",
	}, nil
}

// RemoveInput removes a WebRTC input from a session
func (s *Server) RemoveInput(_ context.Context, req *pb.RemoveInputRequest) (*pb.RemoveInputResponse, error) {
	zlog.Info().Msgf("[%s] RemoveInput: %v", req.SessionId, req)

	zlog.Info().Msgf("[%s][MUTEX] RemoveInput: read lock", req.SessionId)
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()
	zlog.Info().Msgf("[%s][MUTEX] RemoveInput: read unlocked", req.SessionId)

	if !exists {
		return &pb.RemoveInputResponse{
			Success: false,
			Message: "session not found",
		}, status.Error(codes.NotFound, "session not found")
	}

	zlog.Info().Msgf("[%s] RemoveInput: session exist", req.SessionId)
	err := session.RemoveInput(req.JanusSessionId, req.JanusHandleId, req.DisplayName)
	if err != nil {
		zlog.Error().Msgf("[%s] RemoveInput: Failed to remove input: %v", req.SessionId, err)
		return &pb.RemoveInputResponse{
			Success: false,
			Message: fmt.Sprintf("failed to remove input: %v", err),
		}, status.Error(codes.Internal, err.Error())
	}

	return &pb.RemoveInputResponse{
		Success: true,
		Message: "input removed successfully",
	}, nil
}

// SetMute sets the mute status for a participant
func (s *Server) SetMute(_ context.Context, req *pb.SetMuteRequest) (*pb.SetMuteResponse, error) {
	zlog.Info().Msgf("[%s] SetMute: %v", req.SessionId, req)

	zlog.Info().Msgf("[%s][MUTEX] SetMute: read lock", req.SessionId)
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()
	zlog.Info().Msgf("[%s][MUTEX] SetMute: read unlocked", req.SessionId)

	if !exists {
		return &pb.SetMuteResponse{
			Success: false,
			Message: "session not found",
		}, status.Error(codes.NotFound, "session not found")
	}

	err := session.SetMute(req.UserId, req.ClientId, req.Mute)
	if err != nil {
		return &pb.SetMuteResponse{
			Success: false,
			Message: fmt.Sprintf("failed to set mute: %v", err),
		}, status.Error(codes.Internal, err.Error())
	}

	return &pb.SetMuteResponse{
		Success: true,
		Message: "mute status set successfully",
	}, nil
}

// SetVideoOn sets the video on/off status for a participant
func (s *Server) SetVideoOn(_ context.Context, req *pb.SetVideoOnRequest) (*pb.SetVideoOnResponse, error) {
	zlog.Info().Msgf("[%s] SetVideoOn: %v", req.SessionId, req)

	zlog.Info().Msgf("[%s][MUTEX] SetVideoOn: read lock", req.SessionId)
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()
	zlog.Info().Msgf("[%s][MUTEX] SetVideoOn: read unlocked", req.SessionId)

	if !exists {
		return &pb.SetVideoOnResponse{
			Success: false,
			Message: "session not found",
		}, status.Error(codes.NotFound, "session not found")
	}

	err := session.SetVideoOn(req.UserId, req.ClientId, req.VideoOn)
	if err != nil {
		return &pb.SetVideoOnResponse{
			Success: false,
			Message: fmt.Sprintf("failed to set video on: %v", err),
		}, status.Error(codes.Internal, err.Error())
	}

	return &pb.SetVideoOnResponse{
		Success: true,
		Message: "video on status set successfully",
	}, nil
}

// WriteID3Tag writes a custom ID3 tag to the HLS stream
func (s *Server) WriteID3Tag(_ context.Context, req *pb.WriteID3TagRequest) (*pb.WriteID3TagResponse, error) {
	zlog.Info().Msgf("[%s] WriteID3Tag: %v", req.SessionId, req)

	zlog.Info().Msgf("[%s][MUTEX] WriteID3Tag: read lock", req.SessionId)
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()
	zlog.Info().Msgf("[%s][MUTEX] WriteID3Tag: read unlocked", req.SessionId)

	if !exists {
		return &pb.WriteID3TagResponse{
			Success: false,
			Message: "session not found",
		}, status.Error(codes.NotFound, "session not found")
	}

	err := session.WriteID3Tag(req.EventData, req.EventType)
	if err != nil {
		return &pb.WriteID3TagResponse{
			Success: false,
			Message: fmt.Sprintf("failed to write ID3 tag: %v", err),
		}, status.Error(codes.Internal, err.Error())
	}

	return &pb.WriteID3TagResponse{
		Success: true,
		Message: "ID3 tag written successfully",
	}, nil
}

// GetSessionInfo retrieves information about a session
func (s *Server) GetSessionInfo(_ context.Context, req *pb.GetSessionInfoRequest) (*pb.GetSessionInfoResponse, error) {
	zlog.Info().Msgf("[%s] GetSessionInfo: %v", req.SessionId, req)

	zlog.Info().Msgf("[%s][MUTEX] GetSessionInfo: read lock", req.SessionId)
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()
	zlog.Info().Msgf("[%s][MUTEX] GetSessionInfo: read unlocked", req.SessionId)

	if !exists {
		return &pb.GetSessionInfoResponse{
			Success: false,
			Message: "session not found",
		}, status.Error(codes.NotFound, "session not found")
	}

	info, err := session.GetSessionInfo()
	if err != nil {
		return &pb.GetSessionInfoResponse{
			Success: false,
			Message: fmt.Sprintf("failed to get session info: %v", err),
		}, status.Error(codes.Internal, err.Error())
	}

	return &pb.GetSessionInfoResponse{
		Success:          true,
		Message:          "session info retrieved successfully",
		ParticipantCount: int32(info.ParticipantCount),
		ParticipantNames: info.ParticipantNames,
	}, nil
}
