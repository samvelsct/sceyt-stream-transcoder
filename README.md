# StreamBridge

gRPC-controlled WebRTC to HLS stream converter using go-webrtchls.

## Overview

StreamBridge is a gRPC service that provides remote control of WebRTC to HLS stream conversion. It receives commands via gRPC to start/stop streaming sessions, add/remove WebRTC inputs, and manage stream metadata.

## Features

**Server:**
- gRPC API for remote control
- Multiple concurrent HLS sessions
- WebRTC input management (add/remove participants)
- Participant state tracking (mute, video on/off)
- Custom ID3 tag support for metadata
- Session information retrieval
- Thread-safe operations
- Graceful shutdown handling
- Flexible configuration (YAML, environment, flags)

**Clients:**
- Command-line interface (CLI) for easy control
- Go client library and examples
- Python client library and examples
- Integration-ready for any language with gRPC support

## Architecture

```
┌─────────────┐         gRPC          ┌──────────────┐
│   Client    │ ◄───────────────────► │ StreamBridge │
│ Application │                       │    Server    │
└─────────────┘                       └──────┬───────┘
                                              │
                                              ▼
                                      ┌───────────────┐
                                      │ go-webrtchls  │
                                      │   (CGO)       │
                                      └───────┬───────┘
                                              │
                                              ▼
                                      ┌───────────────┐
                                      │ libwebrtc_hls │
                                      │  (C library)  │
                                      └───────┬───────┘
                                              │
                        ┌─────────────────────┼─────────────────────┐
                        ▼                     ▼                     ▼
                  ┌──────────┐         ┌──────────┐         ┌──────────┐
                  │  Janus   │         │  FFmpeg  │         │   HLS    │
                  │ Gateway  │         │   or     │         │  Output  │
                  │ (WebRTC) │         │ GStreamer│         │  Files   │
                  └──────────┘         └──────────┘         └──────────┘
```

## Prerequisites

- Go 1.23+
- CGO enabled
- Protocol Buffers compiler (`protoc`)
- Go gRPC plugins for protoc
- go-webrtchls module
- libwebrtc_hls.so library
- Janus Gateway server
- FFmpeg or GStreamer

## Installation

### 1. Install Dependencies

```bash
# Install protoc compiler
# On Ubuntu/Debian:
sudo apt-get install -y protobuf-compiler

# Install Go protoc plugins
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

### 2. Clone and Build

```bash
cd /home/samvel/Projects/Test/streambridge

# Generate protobuf files and build
make build

# Or build manually
CGO_ENABLED=1 go build -o bin/streambridge ./cmd/streambridge
```

## Configuration

StreamBridge supports configuration via:
1. YAML configuration file
2. Environment variables
3. Command-line flags

Priority: Command-line flags > Environment variables > Config file > Defaults

### Configuration File

Create a `config.yaml` file (see `config.example.yaml`):

```yaml
server:
  port: 50051
  max_concurrent_streams: 100
  connection_timeout: 30s
  enable_reflection: true

janus:
  gateway_address: "ws://localhost:8188"
  admin_key: "adminpwd"
  admin_secret: "admin"
  timeout: 10

hls:
  output_dir: "/tmp/hls"
  segment_duration: 4
  playlist_length: 5
  enable_gstreamer: false

logging:
  level: "info"
  format: "text"
  output: "stdout"
```

### Environment Variables

All configuration options can be set via environment variables:

```bash
# Server configuration
export STREAMBRIDGE_PORT=50051
export STREAMBRIDGE_MAX_STREAMS=100
export STREAMBRIDGE_TIMEOUT=30s
export STREAMBRIDGE_REFLECTION=true

# Janus configuration
export JANUS_GATEWAY_ADDRESS="ws://localhost:8188"
export JANUS_ADMIN_KEY="adminpwd"
export JANUS_ADMIN_SECRET="admin"
export JANUS_TIMEOUT=10

# HLS configuration
export HLS_OUTPUT_DIR="/tmp/hls"
export HLS_SEGMENT_DURATION=4
export HLS_PLAYLIST_LENGTH=5
export HLS_ENABLE_GSTREAMER=false

# Logging configuration
export LOG_LEVEL="info"
export LOG_FORMAT="text"
export LOG_OUTPUT="stdout"
```

### Command-Line Flags

```bash
# Show available flags
./bin/streambridge --help

# Common flags
./bin/streambridge --config config.yaml       # Load config file
./bin/streambridge --port 9090                # Override port
./bin/streambridge --show-config              # Show config and exit
```

## Usage

### Starting the Server

```bash
# Run with defaults
./bin/streambridge

# Run with config file
./bin/streambridge --config config.yaml

# Run with custom port (overrides config)
./bin/streambridge --config config.yaml --port 9090

# Show current configuration
./bin/streambridge --config config.yaml --show-config
```

### Using the gRPC API

#### 1. Create a Session

```bash
# With absolute path
grpcurl -plaintext -d '{
  "session_id": "room-123",
  "output_path": "/tmp/output.m3u8",
  "enable_gst": false
}' localhost:50051 streambridge.StreamBridge/CreateSession

# With relative path (will be joined with HLS output_dir from config)
grpcurl -plaintext -d '{
  "session_id": "room-123",
  "output_path": "room-123/stream.m3u8"
}' localhost:50051 streambridge.StreamBridge/CreateSession
```

#### 2. Add WebRTC Input

```bash
# With full configuration
grpcurl -plaintext -d '{
  "session_id": "room-123",
  "janus_room_id": 1234,
  "janus_session_id": 5678,
  "janus_handle_id": 9012,
  "janus_publisher_id": 3456,
  "janus_gateway_address": "ws://localhost:8188",
  "janus_admin_key": "adminpwd",
  "janus_admin_secret": "admin",
  "display_name": "John Doe"
}' localhost:50051 streambridge.StreamBridge/AddInput

# Using defaults from config (omit janus_gateway_address, janus_admin_key, janus_admin_secret)
grpcurl -plaintext -d '{
  "session_id": "room-123",
  "janus_room_id": 1234,
  "janus_session_id": 5678,
  "janus_handle_id": 9012,
  "janus_publisher_id": 3456,
  "display_name": "John Doe"
}' localhost:50051 streambridge.StreamBridge/AddInput
```

#### 3. Get Session Info

```bash
grpcurl -plaintext -d '{
  "session_id": "room-123"
}' localhost:50051 streambridge.StreamBridge/GetSessionInfo
```

#### 4. Set Participant Mute Status

```bash
grpcurl -plaintext -d '{
  "session_id": "room-123",
  "user_id": "user123",
  "client_id": "client456",
  "mute": true
}' localhost:50051 streambridge.StreamBridge/SetMute
```

#### 5. Write Custom ID3 Tag

```bash
grpcurl -plaintext -d '{
  "session_id": "room-123",
  "event_data": "{\"event\":\"hand-raised\"}",
  "event_type": "custom"
}' localhost:50051 streambridge.StreamBridge/WriteID3Tag
```

#### 6. Remove Input

```bash
grpcurl -plaintext -d '{
  "session_id": "room-123",
  "janus_session_id": 5678,
  "janus_handle_id": 9012,
  "display_name": "John Doe"
}' localhost:50051 streambridge.StreamBridge/RemoveInput
```

#### 7. Destroy Session

```bash
grpcurl -plaintext -d '{
  "session_id": "room-123"
}' localhost:50051 streambridge.StreamBridge/DestroySession
```

## gRPC Service Definition

The service exposes the following RPCs:

- `CreateSession` - Create a new HLS output session
- `DestroySession` - Destroy an existing session
- `AddInput` - Add a WebRTC input to a session
- `RemoveInput` - Remove a WebRTC input from a session
- `SetMute` - Set participant mute status
- `SetVideoOn` - Set participant video on/off status
- `WriteID3Tag` - Write custom ID3 tag to HLS stream
- `GetSessionInfo` - Get session information

See `proto/streambridge.proto` for full API specification.

## Client Tools & Examples

StreamBridge provides multiple ways to interact with the server:

### 1. Command-Line Interface (CLI)

The easiest way to control StreamBridge from the command line:

```bash
# Build CLI
make cli

# Create a session
./bin/streambridge-cli session create room-123 output/stream.m3u8

# Add participant
./bin/streambridge-cli input add room-123 \
  --room 1234 --session 5678 --handle 9012 --publisher 3456 --name "Alice"

# Get session info
./bin/streambridge-cli session info room-123

# Set mute status
./bin/streambridge-cli mute room-123 user-1 client-1 true

# Destroy session
./bin/streambridge-cli session destroy room-123
```

See [cmd/streambridge-cli/README.md](cmd/streambridge-cli/README.md) for complete CLI documentation.

### 2. Go Client Library

Full-featured Go client with examples:

```bash
# Build and run Go example
make examples
./bin/go-client --server localhost:50051
```

See [examples/go-client/README.md](examples/go-client/README.md) for integration guide.

### 3. Python Client

Python library for integration with Python applications:

```bash
cd examples/python-client

# Install dependencies
pip install -r requirements.txt

# Generate protobuf files
python -m grpc_tools.protoc -I../../proto --python_out=. --grpc_python_out=. ../../proto/streambridge.proto

# Run demo
./client.py --server localhost:50051
```

See [examples/python-client/README.md](examples/python-client/README.md) for Python API documentation.

### 4. Quick Go Example

```go
package main

import (
    "context"
    "log"

    pb "vt-stream-gateway/api"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

func main() {
    // Connect to server
    conn, err := grpc.Dial("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
    if err != nil {
        log.Fatal(err)
    }
    defer conn.Close()

    client := pb.NewStreamBridgeClient(conn)

    // Create session
    resp, err := client.CreateSession(context.Background(), &pb.CreateSessionRequest{
        SessionId:  "room-123",
        OutputPath: "/tmp/output.m3u8",
        EnableGst:  false,
    })
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("Session created: %v", resp.Success)

    // Add input
    inputResp, err := client.AddInput(context.Background(), &pb.AddInputRequest{
        SessionId:            "room-123",
        JanusRoomId:          1234,
        JanusSessionId:       5678,
        JanusHandleId:        9012,
        JanusPublisherId:     3456,
        JanusGatewayAddress:  "ws://localhost:8188",
        JanusAdminKey:        "adminpwd",
        JanusAdminSecret:     "admin",
        DisplayName:          "John Doe",
    })
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("Input added: %v", inputResp.Success)
}
```

## Project Structure

```
streambridge/
├── cmd/
│   ├── streambridge/            # Server application
│   │   └── main.go
│   └── streambridge-cli/        # CLI client tool
│       ├── main.go
│       └── README.md
├── examples/
│   ├── go-client/               # Go client example
│   │   ├── main.go
│   │   ├── go.mod
│   │   └── README.md
│   └── python-client/           # Python client example
│       ├── client.py
│       ├── requirements.txt
│       └── README.md
├── internal/
│   ├── config/
│   │   └── config.go            # Configuration management
│   └── server/
│       └── server.go            # gRPC server implementation
├── proto/
│   └── streambridge.proto       # Protocol Buffers definition
├── api/                         # Generated protobuf files (auto-generated)
├── go.mod                       # Go module definition
├── Makefile                     # Build automation
├── config.example.yaml          # Example configuration file
├── CONFIGURATION.md             # Configuration guide
├── .gitignore                   # Git ignore rules
└── README.md                    # This file
```

## Development

### Build Commands

```bash
# Build everything (server + CLI + examples)
make all

# Build server only
make build

# Build CLI client
make cli

# Build example clients
make examples

# Generate protobuf files
make proto

# Clean build artifacts
make clean

# Show help
make help
```

### Running

```bash
# Start server with defaults
./bin/streambridge

# Start server with config
./bin/streambridge --config config.yaml

# Use CLI client
./bin/streambridge-cli session create room-123 output.m3u8

# Run Go example
./bin/go-client

# Run Python example
cd examples/python-client && ./client.py
```

## Configuration Options

### Server Options
- `port` - gRPC server port (default: 50051)
- `max_concurrent_streams` - Maximum concurrent gRPC streams (default: 100)
- `connection_timeout` - Connection timeout for keepalive (default: 30s)
- `enable_reflection` - Enable gRPC reflection for debugging (default: true)

### Janus Options (defaults for AddInput)
- `gateway_address` - Janus Gateway WebSocket address (default: ws://localhost:8188)
- `admin_key` - Janus admin API key (default: adminpwd)
- `admin_secret` - Janus admin secret (default: admin)
- `timeout` - Connection timeout in seconds (default: 10)

### HLS Options
- `output_dir` - Base directory for HLS output files (default: /tmp/hls)
- `segment_duration` - HLS segment duration in seconds (default: 4)
- `playlist_length` - Number of segments in playlist (default: 5)
- `enable_gstreamer` - Use GStreamer instead of FFmpeg (default: false)

### Logging Options
- `level` - Log level: debug, info, warn, error (default: info)
- `format` - Log format: text, json (default: text)
- `output` - Log output: stdout, stderr, or file path (default: stdout)

## Environment Variables

Ensure the libwebrtc_hls library is accessible:

```bash
export LD_LIBRARY_PATH=/home/samvel/Projects/Test/ffmpeg-test:$LD_LIBRARY_PATH
```

Or the library path is configured in go-webrtchls module.

## Error Handling

All gRPC methods return a response with:
- `success` (bool) - Whether the operation succeeded
- `message` (string) - Success or error message

Additional fields may be present depending on the method.

gRPC status codes are also set appropriately:
- `InvalidArgument` - Missing or invalid parameters
- `NotFound` - Session not found
- `AlreadyExists` - Session already exists
- `Internal` - Internal error from webrtchls

## Limitations

- Requires Janus Gateway for WebRTC connectivity
- Sessions must be created before adding inputs
- Session IDs must be unique
- CGO required (cannot cross-compile easily)

## Troubleshooting

### Library not found error

```bash
export LD_LIBRARY_PATH=/path/to/library:$LD_LIBRARY_PATH
```

### CGO disabled error

```bash
export CGO_ENABLED=1
go build
```

### Protoc not found

```bash
# Install protoc compiler for your OS
sudo apt-get install -y protobuf-compiler  # Debian/Ubuntu
```

## License

See LICENSE file.

## Related Projects

- [go-webrtchls](https://github.com/samvel/go-webrtchls) - Go bindings for libwebrtc_hls
- [Janus Gateway](https://janus.conf.meetecho.com/) - WebRTC server

## Support

For issues, please open an issue on GitHub.
