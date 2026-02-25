# Go Client Example

This example demonstrates how to use StreamBridge from a Go application.

## What It Does

The example client performs the following operations:

1. Connects to StreamBridge server
2. Creates an HLS session
3. Adds two WebRTC participants
4. Gets session information
5. Updates participant status (mute/video)
6. Writes custom ID3 tags
7. Removes a participant
8. Destroys the session

## Prerequisites

- StreamBridge server running (default: localhost:50051)
- Go 1.23+

## Build and Run

```bash
# Build
go build -o go-client

# Run with defaults (connects to localhost:50051)
./go-client

# Run with custom server address
./go-client --server localhost:9090
```

## Output Example

```
Connecting to StreamBridge at localhost:50051...
Connected successfully!

=== Creating Session ===
✓ session created successfully

=== Adding WebRTC Input ===
✓ input added successfully

=== Adding Second Participant ===
✓ input added successfully

=== Getting Session Info ===
✓ Participants: 2
  1. Alice
  2. Bob

=== Setting Mute Status ===
✓ mute status set successfully

=== Setting Video Status ===
✓ video on status set successfully

=== Writing Custom ID3 Tag ===
✓ ID3 tag written successfully

=== Session Running ===
Keeping session active for 10 seconds...

=== Removing Input ===
✓ input removed successfully

=== Getting Updated Session Info ===
✓ Participants: 1
  1. Alice

=== Destroying Session ===
✓ session destroyed successfully

=== Demo Complete ===
All operations completed successfully!
```

## Code Structure

The example shows:
- **Connection**: Using `grpc.Dial()` to connect
- **Session Management**: Create and destroy sessions
- **Input Management**: Add and remove WebRTC participants
- **Status Updates**: Mute and video status tracking
- **Metadata**: Custom ID3 tags for events
- **Info Retrieval**: Getting current session state

## Customization

Modify the example to:
- Use real Janus Gateway connection details
- Handle errors differently
- Run multiple sessions concurrently
- Stream for longer periods
- Add more participants

## Integration

Use this as a template for integrating StreamBridge into your application:

```go
import (
    pb "vt-stream-gateway/api"
    "google.golang.org/grpc"
)

// Connect
conn, _ := grpc.Dial("localhost:50051", grpc.WithInsecure())
client := pb.NewStreamBridgeClient(conn)

// Create session
resp, _ := client.CreateSession(ctx, &pb.CreateSessionRequest{
    SessionId:  "my-room",
    OutputPath: "output/stream.m3u8",
})

// Add participants, manage stream, etc.
```
