package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	pb "vt-stream-transcoder/api"
	"vt-stream-transcoder/internal/config"
	"vt-stream-transcoder/internal/httpserver"
	"vt-stream-transcoder/internal/server"
	"vt-stream-transcoder/internal/webrtchls"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

var (
	configFile = flag.String("config", "", "Path to configuration file (YAML)")
	port       = flag.Int("port", 0, "The gRPC server port (overrides config)")
	hlsAddr    = flag.String("hls-addr", ":8080", "Address for the LL-HLS HTTP server")
	showConfig = flag.Bool("show-config", false, "Show current configuration and exit")
)

type sessionIDGetter interface {
	GetSessionId() string
}

func grpcDurationInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	durationMs := float64(time.Since(start).Microseconds()) / 1000.0

	code := codes.OK
	if err != nil {
		if s, ok := status.FromError(err); ok {
			code = s.Code()
		} else {
			code = codes.Unknown
		}
	}

	var sessionID string
	if r, ok := req.(sessionIDGetter); ok {
		sessionID = r.GetSessionId()
	}

	zlog.Info().Msgf("[%s] GRPC request method: %s | %s | %.3fms", sessionID, info.FullMethod, code.String(), durationMs)

	return resp, err
}

func startHealthCheck() {
	go func() {
		http.HandleFunc("/healthcheck", func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusOK)
		})
		_ = http.ListenAndServe(":8080", nil)
	}()
}

func setupSignalHandlers() {
	// Setup signal handler for SIGSEGV to capture backtrace
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGSEGV, syscall.SIGABRT, syscall.SIGBUS, syscall.SIGILL)

	go func() {
		sig := <-sigChan
		log.Printf("!!! FATAL SIGNAL RECEIVED: %v !!!", sig)
		log.Println("=== STACK TRACE ===")
		debug.PrintStack()
		log.Println("=== END STACK TRACE ===")

		// Also write to stderr for immediate visibility
		fmt.Fprintf(os.Stderr, "\n!!! FATAL SIGNAL RECEIVED: %v !!!\n", sig)
		fmt.Fprintf(os.Stderr, "=== STACK TRACE ===\n")
		os.Stderr.Write(debug.Stack())
		fmt.Fprintf(os.Stderr, "=== END STACK TRACE ===\n")

		// Exit with error code
		os.Exit(139)
	}()
}

func main() {
	// Recover from panics with stack trace
	defer func() {
		if r := recover(); r != nil {
			log.Printf("!!! PANIC RECOVERED: %v !!!", r)
			log.Println("=== PANIC STACK TRACE ===")
			debug.PrintStack()
			log.Println("=== END PANIC STACK TRACE ===")

			fmt.Fprintf(os.Stderr, "\n!!! PANIC RECOVERED: %v !!!\n", r)
			fmt.Fprintf(os.Stderr, "=== PANIC STACK TRACE ===\n")
			os.Stderr.Write(debug.Stack())
			fmt.Fprintf(os.Stderr, "=== END PANIC STACK TRACE ===\n")

			os.Exit(2)
		}
	}()

	flag.Parse()

	// Setup signal handlers for crash debugging
	setupSignalHandlers()

	// Load configuration
	cfg := config.LoadOrDefault(*configFile)

	// Override port from command line if specified
	if *port != 0 {
		cfg.Server.Port = *port
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	// Apply log level to the C library.
	// Use lib_level when set, otherwise fall back to the app log level.
	libLogLevel := cfg.Logging.LibLevel
	if libLogLevel == "" {
		libLogLevel = cfg.Logging.Level
	}
	switch libLogLevel {
	case "debug":
		webrtchls.SetLogLevel(webrtchls.LogLevelDebug)
	case "warn":
		webrtchls.SetLogLevel(webrtchls.LogLevelWarn)
	case "error":
		webrtchls.SetLogLevel(webrtchls.LogLevelError)
	default:
		webrtchls.SetLogLevel(webrtchls.LogLevelInfo)
	}

	if cfg.Logging.Format == "text" {
		zerolog.TimeFieldFormat = time.RFC3339Nano
		// Custom console writer with HH:MM:SS.mmm time format
		consoleWriter := zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: "15:04:05.000", // HH:MM:SS.milliseconds format
		}
		zlog.Logger = zlog.With().Caller().Logger().Output(consoleWriter)
		webrtchls.SetLogFormat(webrtchls.LogFormatText)
	} else {
		zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
		webrtchls.SetLogFormat(webrtchls.LogFormatJSON)
	}

	// Show config and exit if requested
	if *showConfig {
		fmt.Println("Current Configuration:")
		fmt.Printf("  Server Port: %d\n", cfg.Server.Port)
		fmt.Printf("  Max Concurrent Streams: %d\n", cfg.Server.MaxConcurrentStreams)
		fmt.Printf("  Connection Timeout: %s\n", cfg.Server.ConnectionTimeout)
		fmt.Printf("  Enable Reflection: %v\n", cfg.Server.EnableReflection)
		fmt.Printf("\n  Janus Gateway: %s\n", cfg.Janus.GatewayAddress)
		fmt.Printf("  Janus Admin Key: %s\n", cfg.Janus.AdminKey)
		fmt.Printf("\n  HLS Output Dir: %s\n", cfg.HLS.OutputDir)
		fmt.Printf("  HLS Segment Duration: %d seconds\n", cfg.HLS.SegmentDuration)
		fmt.Printf("  HLS Playlist Window: %d segments\n", cfg.HLS.PlaylistWindow)
		fmt.Printf("  Enable GStreamer: %v\n", cfg.HLS.EnableGStreamer)
		fmt.Printf("\n  Log Level: %s\n", cfg.Logging.Level)
		fmt.Printf("  Log Format: %s\n", cfg.Logging.Format)
		fmt.Printf("  Log Output: %s\n", cfg.Logging.Output)
		fmt.Printf("\n  Lib Log Level: %s\n", cfg.Logging.LibLevel)
		return
	}

	// Ensure HLS output directory exists
	if err := os.MkdirAll(cfg.HLS.OutputDir, 0755); err != nil {
		log.Fatalf("Failed to create HLS output directory: %v", err)
	}

	// Create listener
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Server.Port))
	if err != nil {
		zlog.Fatal().Msgf("Failed to listen on port %d: %v", cfg.Server.Port, err)
	}

	// Start LL-HLS HTTP server.
	hlsSrv := httpserver.New(*hlsAddr, &cfg.HLS)
	hlsSrv.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hlsSrv.Stop(ctx)
	}()
	zlog.Info().Msgf("LL-HLS HTTP server listening on %s", *hlsAddr)

	// Create StreamBridge server with config
	zlog.Info().Msgf("Creating StreamBridge server...")
	streamBridgeServer, err := server.NewServer(cfg, hlsSrv)
	if err != nil {
		log.Printf("Failed to create server: %v", err)
		log.Println("Stack trace at error:")
		debug.PrintStack()
		log.Fatalf("Failed to create server: %v", err)
	}
	zlog.Info().Msgf("StreamBridge server created successfully")
	defer streamBridgeServer.Close()

	// Create gRPC server with options
	grpcOpts := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(cfg.Server.MaxConcurrentStreams),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    cfg.Server.ConnectionTimeout,
			Timeout: cfg.Server.ConnectionTimeout / 2,
		}),
		grpc.UnaryInterceptor(grpcDurationInterceptor),
	}
	grpcServer := grpc.NewServer(grpcOpts...)
	pb.RegisterStreamBridgeServer(grpcServer, streamBridgeServer)

	// Register reflection service for grpc_cli and debugging
	if cfg.Server.EnableReflection {
		reflection.Register(grpcServer)
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		zlog.Info().Msgf("Received shutdown signal, stopping server...")
		grpcServer.GracefulStop()
	}()

	startHealthCheck()

	zlog.Info().Msgf("StreamBridge gRPC server starting...")
	zlog.Info().Msgf("  Port: %d", cfg.Server.Port)
	zlog.Info().Msgf("  HLS Output: %s", cfg.HLS.OutputDir)
	zlog.Info().Msgf("  Janus Gateway: %s", cfg.Janus.GatewayAddress)
	zlog.Info().Msgf("  Log Level: %s", cfg.Logging.Level)
	zlog.Info().Msgf("  Lib Log Level: %s", cfg.Logging.LibLevel)
	zlog.Info().Msgf("Ready to receive commands for WebRTC to HLS conversion")

	if err := grpcServer.Serve(lis); err != nil {
		zlog.Fatal().Msgf("Failed to serve: %v", err)
	}
}
