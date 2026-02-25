package server

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	pb "vt-stream-transcoder/api"
	"vt-stream-transcoder/internal/config"

	"github.com/samvelsct/go-webrtchls/webrtchls"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the StreamBridge gRPC service
type Server struct {
	pb.UnimplementedStreamBridgeServer
	ctx      *webrtchls.Context
	sessions map[string]*webrtchls.Session
	config   *config.Config
	mu       sync.RWMutex
}

// NewServer creates a new StreamBridge server
func NewServer(cfg *config.Config) (*Server, error) {
	// Initialize the webrtchls library
	if err := webrtchls.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize webrtchls: %w", err)
	}

	// Create a context
	ctx := webrtchls.NewContext()
	if ctx == nil {
		webrtchls.Cleanup()
		return nil, fmt.Errorf("failed to create webrtchls context")
	}

	return &Server{
		ctx:      ctx,
		sessions: make(map[string]*webrtchls.Session),
		config:   cfg,
	}, nil
}

// Close cleans up the server resources
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

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
func (s *Server) CreateSession(ctx context.Context, req *pb.CreateSessionRequest) (*pb.CreateSessionResponse, error) {
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

	s.mu.Lock()
	defer s.mu.Unlock()

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

	// Use configured GStreamer setting if not specified in request
	enableGst := req.EnableGst
	if !req.EnableGst && s.config.HLS.EnableGStreamer {
		enableGst = true
	}

	// Create the session
	session, err := s.ctx.CreateSession(&webrtchls.SessionConfig{
		SessionID:  req.SessionId,
		OutputPath: outputPath,
		EnableGst:  enableGst,
	})
	if err != nil {
		return &pb.CreateSessionResponse{
			Success: false,
			Message: fmt.Sprintf("failed to create session: %v", err),
		}, status.Error(codes.Internal, err.Error())
	}

	s.sessions[req.SessionId] = session

	return &pb.CreateSessionResponse{
		Success: true,
		Message: "session created successfully",
	}, nil
}

// DestroySession destroys an existing session
func (s *Server) DestroySession(ctx context.Context, req *pb.DestroySessionRequest) (*pb.DestroySessionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, exists := s.sessions[req.SessionId]
	if !exists {
		return &pb.DestroySessionResponse{
			Success: false,
			Message: "session not found",
		}, status.Error(codes.NotFound, "session not found")
	}

	session.Destroy()
	delete(s.sessions, req.SessionId)

	return &pb.DestroySessionResponse{
		Success: true,
		Message: "session destroyed successfully",
	}, nil
}

// AddInput adds a WebRTC input to a session
func (s *Server) AddInput(ctx context.Context, req *pb.AddInputRequest) (*pb.AddInputResponse, error) {
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()

	if !exists {
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

	err := session.AddInput(&webrtchls.InputConfig{
		JanusRoomID:         req.JanusRoomId,
		JanusSessionID:      req.JanusSessionId,
		JanusHandleID:       req.JanusHandleId,
		JanusPublisherID:    req.JanusPublisherId,
		JanusGatewayAddress: janusGateway,
		JanusAdminKey:       janusAdminKey,
		JanusAdminSecret:    janusAdminSecret,
		DisplayName:         req.DisplayName,
	})
	if err != nil {
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
func (s *Server) RemoveInput(ctx context.Context, req *pb.RemoveInputRequest) (*pb.RemoveInputResponse, error) {
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()

	if !exists {
		return &pb.RemoveInputResponse{
			Success: false,
			Message: "session not found",
		}, status.Error(codes.NotFound, "session not found")
	}

	err := session.RemoveInput(req.JanusSessionId, req.JanusHandleId, req.DisplayName)
	if err != nil {
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
func (s *Server) SetMute(ctx context.Context, req *pb.SetMuteRequest) (*pb.SetMuteResponse, error) {
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()

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
func (s *Server) SetVideoOn(ctx context.Context, req *pb.SetVideoOnRequest) (*pb.SetVideoOnResponse, error) {
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()

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
func (s *Server) WriteID3Tag(ctx context.Context, req *pb.WriteID3TagRequest) (*pb.WriteID3TagResponse, error) {
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()

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
func (s *Server) GetSessionInfo(ctx context.Context, req *pb.GetSessionInfoRequest) (*pb.GetSessionInfoResponse, error) {
	s.mu.RLock()
	session, exists := s.sessions[req.SessionId]
	s.mu.RUnlock()

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
