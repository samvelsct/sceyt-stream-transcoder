package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	pb "vt-stream-transcoder/api"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	serverAddr = flag.String("server", "localhost:50051", "StreamBridge server address")
	timeout    = flag.Duration("timeout", 10*time.Second, "Request timeout")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "StreamBridge CLI - Command-line client for StreamBridge\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <command> [args...]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nCommands:\n")
		fmt.Fprintf(os.Stderr, "  session create <session-id> <output-path> [--gst]  Create a new session\n")
		fmt.Fprintf(os.Stderr, "  session destroy <session-id>                        Destroy a session\n")
		fmt.Fprintf(os.Stderr, "  session info <session-id>                           Get session info\n")
		fmt.Fprintf(os.Stderr, "  input add <session-id> <params...>                  Add WebRTC input\n")
		fmt.Fprintf(os.Stderr, "  input remove <session-id> <janus-session> <janus-handle> <display-name>\n")
		fmt.Fprintf(os.Stderr, "  mute <session-id> <user-id> <client-id> <true|false>\n")
		fmt.Fprintf(os.Stderr, "  video <session-id> <user-id> <client-id> <true|false>\n")
		fmt.Fprintf(os.Stderr, "  tag <session-id> <event-data> <event-type>          Write ID3 tag\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  # Create session\n")
		fmt.Fprintf(os.Stderr, "  %s session create room-123 output/stream.m3u8\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # Add input\n")
		fmt.Fprintf(os.Stderr, "  %s input add room-123 --room 1234 --session 5678 --handle 9012 --publisher 3456 --name Alice\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # Get info\n")
		fmt.Fprintf(os.Stderr, "  %s session info room-123\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # Set mute\n")
		fmt.Fprintf(os.Stderr, "  %s mute room-123 user-1 client-1 true\n\n", os.Args[0])
	}

	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	// Connect to server
	conn, err := grpc.Dial(*serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(*timeout),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to %s: %v\n", *serverAddr, err)
		os.Exit(1)
	}
	defer conn.Close()

	client := pb.NewStreamBridgeClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Parse and execute command
	command := flag.Arg(0)
	args := flag.Args()[1:]

	switch command {
	case "session":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "Error: session command requires a subcommand")
			flag.Usage()
			os.Exit(1)
		}
		handleSessionCommand(ctx, client, args)

	case "input":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "Error: input command requires a subcommand")
			flag.Usage()
			os.Exit(1)
		}
		handleInputCommand(ctx, client, args)

	case "mute":
		if len(args) != 4 {
			fmt.Fprintln(os.Stderr, "Error: mute requires: <session-id> <user-id> <client-id> <true|false>")
			os.Exit(1)
		}
		handleMuteCommand(ctx, client, args)

	case "video":
		if len(args) != 4 {
			fmt.Fprintln(os.Stderr, "Error: video requires: <session-id> <user-id> <client-id> <true|false>")
			os.Exit(1)
		}
		handleVideoCommand(ctx, client, args)

	case "tag":
		if len(args) != 3 {
			fmt.Fprintln(os.Stderr, "Error: tag requires: <session-id> <event-data> <event-type>")
			os.Exit(1)
		}
		handleTagCommand(ctx, client, args)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		flag.Usage()
		os.Exit(1)
	}
}

func handleSessionCommand(ctx context.Context, client pb.StreamBridgeClient, args []string) {
	subCmd := args[0]
	subArgs := args[1:]

	switch subCmd {
	case "create":
		if len(subArgs) < 2 {
			fmt.Fprintln(os.Stderr, "Error: session create requires: <session-id> <output-path> [--gst]")
			os.Exit(1)
		}
		sessionID := subArgs[0]
		outputPath := subArgs[1]
		enableGst := len(subArgs) > 2 && subArgs[2] == "--gst"

		resp, err := client.CreateSession(ctx, &pb.CreateSessionRequest{
			SessionId:  sessionID,
			OutputPath: outputPath,
			EnableGst:  enableGst,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ %s\n", resp.Message)

	case "destroy":
		if len(subArgs) != 1 {
			fmt.Fprintln(os.Stderr, "Error: session destroy requires: <session-id>")
			os.Exit(1)
		}
		resp, err := client.DestroySession(ctx, &pb.DestroySessionRequest{
			SessionId: subArgs[0],
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ %s\n", resp.Message)

	case "info":
		if len(subArgs) != 1 {
			fmt.Fprintln(os.Stderr, "Error: session info requires: <session-id>")
			os.Exit(1)
		}
		resp, err := client.GetSessionInfo(ctx, &pb.GetSessionInfoRequest{
			SessionId: subArgs[0],
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Session: %s\n", subArgs[0])
		fmt.Printf("Participants: %d\n", resp.ParticipantCount)
		for i, name := range resp.ParticipantNames {
			fmt.Printf("  %d. %s\n", i+1, name)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown session subcommand: %s\n", subCmd)
		os.Exit(1)
	}
}

func handleInputCommand(ctx context.Context, client pb.StreamBridgeClient, args []string) {
	subCmd := args[0]
	subArgs := args[1:]

	switch subCmd {
	case "add":
		if len(subArgs) < 1 {
			fmt.Fprintln(os.Stderr, "Error: input add requires session-id and flags")
			os.Exit(1)
		}

		sessionID := subArgs[0]
		flags := parseInputFlags(subArgs[1:])

		resp, err := client.AddInput(ctx, &pb.AddInputRequest{
			SessionId:           sessionID,
			JanusRoomId:         flags.roomID,
			JanusSessionId:      flags.sessionID,
			JanusHandleId:       flags.handleID,
			JanusPublisherId:    flags.publisherID,
			JanusGatewayAddress: flags.gateway,
			JanusAdminKey:       flags.adminKey,
			JanusAdminSecret:    flags.adminSecret,
			DisplayName:         flags.displayName,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ %s\n", resp.Message)

	case "remove":
		if len(subArgs) != 4 {
			fmt.Fprintln(os.Stderr, "Error: input remove requires: <session-id> <janus-session> <janus-handle> <display-name>")
			os.Exit(1)
		}
		sessionID := subArgs[0]
		janusSession, _ := strconv.ParseUint(subArgs[1], 10, 64)
		janusHandle, _ := strconv.ParseUint(subArgs[2], 10, 64)
		displayName := subArgs[3]

		resp, err := client.RemoveInput(ctx, &pb.RemoveInputRequest{
			SessionId:      sessionID,
			JanusSessionId: janusSession,
			JanusHandleId:  janusHandle,
			DisplayName:    displayName,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ %s\n", resp.Message)

	default:
		fmt.Fprintf(os.Stderr, "Unknown input subcommand: %s\n", subCmd)
		os.Exit(1)
	}
}

func handleMuteCommand(ctx context.Context, client pb.StreamBridgeClient, args []string) {
	mute := strings.ToLower(args[3]) == "true"
	resp, err := client.SetMute(ctx, &pb.SetMuteRequest{
		SessionId: args[0],
		UserId:    args[1],
		ClientId:  args[2],
		Mute:      mute,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ %s\n", resp.Message)
}

func handleVideoCommand(ctx context.Context, client pb.StreamBridgeClient, args []string) {
	videoOn := strings.ToLower(args[3]) == "true"
	resp, err := client.SetVideoOn(ctx, &pb.SetVideoOnRequest{
		SessionId: args[0],
		UserId:    args[1],
		ClientId:  args[2],
		VideoOn:   videoOn,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ %s\n", resp.Message)
}

func handleTagCommand(ctx context.Context, client pb.StreamBridgeClient, args []string) {
	resp, err := client.WriteID3Tag(ctx, &pb.WriteID3TagRequest{
		SessionId: args[0],
		EventData: args[1],
		EventType: args[2],
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ %s\n", resp.Message)
}

type inputFlags struct {
	roomID      uint64
	sessionID   uint64
	handleID    uint64
	publisherID uint64
	gateway     string
	adminKey    string
	adminSecret string
	displayName string
}

func parseInputFlags(args []string) inputFlags {
	var flags inputFlags
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			continue
		}
		key := strings.TrimPrefix(args[i], "--")
		if i+1 >= len(args) {
			continue
		}
		value := args[i+1]
		i++

		switch key {
		case "room":
			flags.roomID, _ = strconv.ParseUint(value, 10, 64)
		case "session":
			flags.sessionID, _ = strconv.ParseUint(value, 10, 64)
		case "handle":
			flags.handleID, _ = strconv.ParseUint(value, 10, 64)
		case "publisher":
			flags.publisherID, _ = strconv.ParseUint(value, 10, 64)
		case "gateway":
			flags.gateway = value
		case "key":
			flags.adminKey = value
		case "secret":
			flags.adminSecret = value
		case "name":
			flags.displayName = value
		}
	}
	return flags
}

func printJSON(v interface{}) {
	data, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(data))
}
