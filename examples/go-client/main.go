package main

import (
	"context"
	"flag"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pb "vt-stream-gateway/api"
)

var (
	serverAddr = flag.String("server", "localhost:50051", "StreamBridge server address")
)

func main() {
	flag.Parse()

	// Connect to StreamBridge server
	log.Printf("Connecting to StreamBridge at %s...", *serverAddr)
	conn, err := grpc.Dial(*serverAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(10*time.Second),
	)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	client := pb.NewStreamBridgeClient(conn)
	log.Println("Connected successfully!")

	// Example 1: Create a session
	log.Println("\n=== Creating Session ===")
	sessionResp, err := client.CreateSession(context.Background(), &pb.CreateSessionRequest{
		SessionId:  "demo-room-123",
		OutputPath: "demo/stream.m3u8",
		EnableGst:  false,
	})
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}
	log.Printf("✓ %s", sessionResp.Message)

	// Example 2: Add a WebRTC input
	log.Println("\n=== Adding WebRTC Input ===")
	inputResp, err := client.AddInput(context.Background(), &pb.AddInputRequest{
		SessionId:        "demo-room-123",
		JanusRoomId:      1234,
		JanusSessionId:   5678,
		JanusHandleId:    9012,
		JanusPublisherId: 3456,
		// Omit Janus connection details to use server defaults
		DisplayName: "Alice",
	})
	if err != nil {
		log.Fatalf("Failed to add input: %v", err)
	}
	log.Printf("✓ %s", inputResp.Message)

	// Example 3: Add another participant
	log.Println("\n=== Adding Second Participant ===")
	input2Resp, err := client.AddInput(context.Background(), &pb.AddInputRequest{
		SessionId:        "demo-room-123",
		JanusRoomId:      1234,
		JanusSessionId:   5679,
		JanusHandleId:    9013,
		JanusPublisherId: 3457,
		DisplayName:      "Bob",
	})
	if err != nil {
		log.Fatalf("Failed to add second input: %v", err)
	}
	log.Printf("✓ %s", input2Resp.Message)

	// Example 4: Get session info
	log.Println("\n=== Getting Session Info ===")
	infoResp, err := client.GetSessionInfo(context.Background(), &pb.GetSessionInfoRequest{
		SessionId: "demo-room-123",
	})
	if err != nil {
		log.Fatalf("Failed to get session info: %v", err)
	}
	log.Printf("✓ Participants: %d", infoResp.ParticipantCount)
	for i, name := range infoResp.ParticipantNames {
		log.Printf("  %d. %s", i+1, name)
	}

	// Example 5: Set participant mute status
	log.Println("\n=== Setting Mute Status ===")
	muteResp, err := client.SetMute(context.Background(), &pb.SetMuteRequest{
		SessionId: "demo-room-123",
		UserId:    "user-alice",
		ClientId:  "client-001",
		Mute:      true,
	})
	if err != nil {
		log.Fatalf("Failed to set mute: %v", err)
	}
	log.Printf("✓ %s", muteResp.Message)

	// Example 6: Set video status
	log.Println("\n=== Setting Video Status ===")
	videoResp, err := client.SetVideoOn(context.Background(), &pb.SetVideoOnRequest{
		SessionId: "demo-room-123",
		UserId:    "user-bob",
		ClientId:  "client-002",
		VideoOn:   false,
	})
	if err != nil {
		log.Fatalf("Failed to set video status: %v", err)
	}
	log.Printf("✓ %s", videoResp.Message)

	// Example 7: Write custom ID3 tag
	log.Println("\n=== Writing Custom ID3 Tag ===")
	tagResp, err := client.WriteID3Tag(context.Background(), &pb.WriteID3TagRequest{
		SessionId: "demo-room-123",
		EventData: `{"event":"hand-raised","userId":"user-alice","timestamp":"2024-02-19T10:30:00Z"}`,
		EventType: "custom-event",
	})
	if err != nil {
		log.Fatalf("Failed to write ID3 tag: %v", err)
	}
	log.Printf("✓ %s", tagResp.Message)

	// Keep session running for a bit
	log.Println("\n=== Session Running ===")
	log.Println("Keeping session active for 10 seconds...")
	time.Sleep(10 * time.Second)

	// Example 8: Remove an input
	log.Println("\n=== Removing Input ===")
	removeResp, err := client.RemoveInput(context.Background(), &pb.RemoveInputRequest{
		SessionId:      "demo-room-123",
		JanusSessionId: 5679,
		JanusHandleId:  9013,
		DisplayName:    "Bob",
	})
	if err != nil {
		log.Fatalf("Failed to remove input: %v", err)
	}
	log.Printf("✓ %s", removeResp.Message)

	// Example 9: Get updated session info
	log.Println("\n=== Getting Updated Session Info ===")
	infoResp2, err := client.GetSessionInfo(context.Background(), &pb.GetSessionInfoRequest{
		SessionId: "demo-room-123",
	})
	if err != nil {
		log.Fatalf("Failed to get session info: %v", err)
	}
	log.Printf("✓ Participants: %d", infoResp2.ParticipantCount)
	for i, name := range infoResp2.ParticipantNames {
		log.Printf("  %d. %s", i+1, name)
	}

	// Example 10: Destroy the session
	log.Println("\n=== Destroying Session ===")
	destroyResp, err := client.DestroySession(context.Background(), &pb.DestroySessionRequest{
		SessionId: "demo-room-123",
	})
	if err != nil {
		log.Fatalf("Failed to destroy session: %v", err)
	}
	log.Printf("✓ %s", destroyResp.Message)

	log.Println("\n=== Demo Complete ===")
	log.Println("All operations completed successfully!")
}
