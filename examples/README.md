# StreamBridge Client Examples

This directory contains example clients for controlling StreamBridge in various languages and scenarios.

## Available Examples

### 1. Go Client ([go-client/](go-client/))

Full-featured Go client demonstrating all StreamBridge operations.

**Features:**
- Complete API usage demonstration
- Session management
- Participant handling
- Status updates (mute, video)
- Custom ID3 tags
- Error handling

**Usage:**
```bash
cd go-client
go build
./go-client --server localhost:50051
```

**Best for:**
- Go applications
- System integration
- Backend services
- Microservices architecture

### 2. Python Client ([python-client/](python-client/))

Python library and example for Python-based applications.

**Features:**
- Easy-to-use Python API
- Full StreamBridge functionality
- Async support (via grpc.aio)
- Integration examples (Flask, async)
- Type hints

**Usage:**
```bash
cd python-client
pip install -r requirements.txt
python -m grpc_tools.protoc -I../../proto --python_out=. --grpc_python_out=. ../../proto/streambridge.proto
./client.py --server localhost:50051
```

**Best for:**
- Python applications
- Web frameworks (Flask, Django, FastAPI)
- Data processing pipelines
- Automation scripts

## Quick Start

### Build All Examples

From the project root:
```bash
make examples
```

This builds:
- Go client → `bin/go-client`

### Run Examples

**Go:**
```bash
./bin/go-client
```

**Python:**
```bash
cd examples/python-client
./client.py
```

## Example Output

All examples perform similar operations:

```
Connected to StreamBridge at localhost:50051

=== Creating Session ===
✓ session created successfully

=== Adding WebRTC Input ===
✓ input added successfully

=== Getting Session Info ===
✓ Participants: 2
  1. Alice
  2. Bob

=== Setting Mute Status ===
✓ mute status set successfully

=== Writing Custom ID3 Tag ===
✓ ID3 tag written successfully

=== Destroying Session ===
✓ session destroyed successfully
```

## Integration Patterns

### 1. Web Application Backend

**Go (HTTP API):**
```go
// examples/go-client as a library
import "vt-stream-gateway/examples/go-client"

func handleCreateRoom(w http.ResponseWriter, r *http.Request) {
    conn, _ := grpc.Dial("localhost:50051", grpc.WithInsecure())
    client := pb.NewStreamBridgeClient(conn)

    resp, _ := client.CreateSession(ctx, &pb.CreateSessionRequest{
        SessionId: roomID,
        OutputPath: fmt.Sprintf("%s/stream.m3u8", roomID),
    })

    json.NewEncoder(w).Encode(resp)
}
```

**Python (Flask):**
```python
# examples/python-client as a library
from flask import Flask, request, jsonify
from client import StreamBridgeClient

app = Flask(__name__)
sb_client = StreamBridgeClient("localhost:50051")

@app.route("/rooms", methods=["POST"])
def create_room():
    data = request.json
    success = sb_client.create_session(
        session_id=data["room_id"],
        output_path=f"{data['room_id']}/stream.m3u8"
    )
    return jsonify({"success": success})
```

### 2. Event-Driven System

**Go (NATS/Kafka consumer):**
```go
// Listen for events and control streams
func handleParticipantJoined(msg *nats.Msg) {
    var event ParticipantEvent
    json.Unmarshal(msg.Data, &event)

    sb_client.AddInput(ctx, &pb.AddInputRequest{
        SessionId: event.RoomID,
        // ... participant details
    })
}
```

**Python (RabbitMQ consumer):**
```python
# React to queue messages
def on_message(ch, method, properties, body):
    event = json.loads(body)
    sb_client.add_input(
        session_id=event["room_id"],
        # ... participant details
    )
```

### 3. CLI Automation

**Bash script:**
```bash
#!/bin/bash
# Create multiple rooms from config

while IFS=, read -r room_id output_path; do
    streambridge-cli session create "$room_id" "$output_path"
done < rooms.csv
```

**Python script:**
```python
#!/usr/bin/env python3
# Bulk operations
import csv
from client import StreamBridgeClient

client = StreamBridgeClient("localhost:50051")

with open("rooms.csv") as f:
    for row in csv.DictReader(f):
        client.create_session(
            session_id=row["room_id"],
            output_path=row["output_path"]
        )
```

## Language Support

StreamBridge uses gRPC, which supports many languages:

- **Go** - Native support (see [go-client/](go-client/))
- **Python** - Native support (see [python-client/](python-client/))
- **JavaScript/TypeScript** - Use `@grpc/grpc-js`
- **Java** - Use `grpc-java`
- **C#/.NET** - Use `Grpc.Net.Client`
- **Ruby** - Use `grpc` gem
- **PHP** - Use `grpc/grpc`
- **Rust** - Use `tonic`

### Generating Clients for Other Languages

**JavaScript:**
```bash
npm install @grpc/grpc-js @grpc/proto-loader
# Use proto-loader to load streambridge.proto at runtime
```

**TypeScript:**
```bash
npm install @grpc/grpc-js
npm install -g grpc-tools
grpc_tools_node_protoc --js_out=import_style=commonjs,binary:. \
    --grpc_out=grpc_js:. \
    --plugin=protoc-gen-grpc=`which grpc_tools_node_protoc_plugin` \
    ../../proto/streambridge.proto
```

**Java:**
```xml
<!-- Add to pom.xml -->
<dependency>
    <groupId>io.grpc</groupId>
    <artifactId>grpc-protobuf</artifactId>
</dependency>
```

## Common Patterns

### Connection Management

**Reusable connection:**
```go
// Create once, reuse
conn := grpc.Dial("localhost:50051", grpc.WithInsecure())
defer conn.Close()
client := pb.NewStreamBridgeClient(conn)
// Use client for multiple requests
```

**Connection pool:**
```python
# Multiple connections for concurrent requests
from concurrent.futures import ThreadPoolExecutor

def create_clients(n):
    return [StreamBridgeClient("localhost:50051") for _ in range(n)]

clients = create_clients(10)
```

### Error Handling

**Go:**
```go
resp, err := client.CreateSession(ctx, req)
if err != nil {
    if st, ok := status.FromError(err); ok {
        switch st.Code() {
        case codes.AlreadyExists:
            // Handle duplicate
        case codes.Unavailable:
            // Retry logic
        }
    }
}
```

**Python:**
```python
try:
    client.create_session("room-123", "output.m3u8")
except grpc.RpcError as e:
    if e.code() == grpc.StatusCode.ALREADY_EXISTS:
        # Handle duplicate
    elif e.code() == grpc.StatusCode.UNAVAILABLE:
        # Retry logic
```

### Timeouts

**Go:**
```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
resp, err := client.CreateSession(ctx, req)
```

**Python:**
```python
response = stub.CreateSession(
    request,
    timeout=5.0  # seconds
)
```

## Testing

### Unit Tests

**Go:**
```go
// Mock StreamBridge client
type MockClient struct {
    pb.UnimplementedStreamBridgeServer
}

func TestSessionCreation(t *testing.T) {
    // Test with mock
}
```

**Python:**
```python
# Mock with unittest.mock
from unittest.mock import MagicMock

def test_session_creation():
    client = StreamBridgeClient("localhost:50051")
    client.stub = MagicMock()
    # Test with mock
```

### Integration Tests

Start StreamBridge server, run tests against it:

```bash
# Terminal 1: Start server
./bin/streambridge

# Terminal 2: Run tests
go test ./examples/go-client/...
pytest examples/python-client/test_*.py
```

## Performance Tips

1. **Reuse connections** - Don't create new connections for each request
2. **Use connection pools** - For high-concurrency scenarios
3. **Set appropriate timeouts** - Based on your network conditions
4. **Handle errors gracefully** - Implement retry logic with exponential backoff
5. **Use keepalive** - For long-lived connections

## Debugging

**Enable gRPC logging:**

Go:
```bash
GRPC_GO_LOG_VERBOSITY_LEVEL=99 GRPC_GO_LOG_SEVERITY_LEVEL=info ./go-client
```

Python:
```python
import logging
logging.basicConfig(level=logging.DEBUG)
```

**Use grpcurl for testing:**
```bash
grpcurl -plaintext localhost:50051 list
grpcurl -plaintext localhost:50051 streambridge.StreamBridge/GetSessionInfo
```

## Next Steps

1. **Choose your client** - Pick the language that fits your stack
2. **Run the example** - Test connectivity and understand the flow
3. **Integrate** - Use the example as a template for your application
4. **Customize** - Adapt error handling, timeouts, and logic to your needs

## Support

For issues or questions:
- Check individual example READMEs
- Review the main [project README](../README.md)
- See [CONFIGURATION.md](../CONFIGURATION.md) for server setup
