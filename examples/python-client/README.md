# Python Client Example

This example demonstrates how to use StreamBridge from Python applications.

## Features

- Full gRPC client implementation
- Easy-to-use Python API
- Complete demo showing all operations
- Error handling and connection management

## Prerequisites

- Python 3.8+
- StreamBridge server running
- pip (Python package manager)

## Installation

```bash
# Install dependencies
pip install -r requirements.txt

# Generate Python protobuf files
python -m grpc_tools.protoc \
  -I../../proto \
  --python_out=. \
  --grpc_python_out=. \
  ../../proto/streambridge.proto
```

This generates:
- `streambridge_pb2.py` - Protocol buffer messages
- `streambridge_pb2_grpc.py` - gRPC service stubs

## Usage

### Run the Demo

```bash
# Make executable
chmod +x client.py

# Run with defaults (localhost:50051)
./client.py

# Run with custom server
./client.py --server localhost:9090
```

### Use as a Library

```python
from client import StreamBridgeClient

# Connect to server
client = StreamBridgeClient("localhost:50051")

# Create session
client.create_session(
    session_id="my-room",
    output_path="output/stream.m3u8"
)

# Add participant
client.add_input(
    session_id="my-room",
    janus_room_id=1234,
    janus_session_id=5678,
    janus_handle_id=9012,
    janus_publisher_id=3456,
    display_name="John Doe"
)

# Get session info
info = client.get_session_info("my-room")
print(f"Participants: {info['participant_count']}")

# Clean up
client.destroy_session("my-room")
client.close()
```

## API Reference

### StreamBridgeClient

```python
class StreamBridgeClient:
    def __init__(self, server_address: str = "localhost:50051")

    def create_session(self, session_id: str, output_path: str,
                      enable_gst: bool = False) -> bool

    def destroy_session(self, session_id: str) -> bool

    def add_input(self, session_id: str, janus_room_id: int,
                 janus_session_id: int, janus_handle_id: int,
                 janus_publisher_id: int, display_name: str,
                 janus_gateway_address: str = "",
                 janus_admin_key: str = "",
                 janus_admin_secret: str = "") -> bool

    def remove_input(self, session_id: str, janus_session_id: int,
                    janus_handle_id: int, display_name: str) -> bool

    def set_mute(self, session_id: str, user_id: str,
                client_id: str, mute: bool) -> bool

    def set_video_on(self, session_id: str, user_id: str,
                    client_id: str, video_on: bool) -> bool

    def write_id3_tag(self, session_id: str, event_data: str,
                     event_type: str) -> bool

    def get_session_info(self, session_id: str) -> Optional[dict]

    def close(self)
```

## Example Output

```
Connected to StreamBridge at localhost:50051

=== Creating Session ===
✓ session created successfully

=== Adding First Participant ===
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

=== Removing Participant ===
✓ input removed successfully

=== Getting Updated Session Info ===
✓ Participants: 1
  1. Alice

=== Destroying Session ===
✓ session destroyed successfully

=== Demo Complete ===
All operations completed successfully!
```

## Integration Examples

### Flask Web Application

```python
from flask import Flask, request, jsonify
from client import StreamBridgeClient

app = Flask(__name__)
client = StreamBridgeClient("localhost:50051")

@app.route("/session/create", methods=["POST"])
def create_session():
    data = request.json
    success = client.create_session(
        session_id=data["session_id"],
        output_path=data["output_path"]
    )
    return jsonify({"success": success})

@app.route("/session/<session_id>/participant", methods=["POST"])
def add_participant(session_id):
    data = request.json
    success = client.add_input(
        session_id=session_id,
        janus_room_id=data["janus_room_id"],
        janus_session_id=data["janus_session_id"],
        janus_handle_id=data["janus_handle_id"],
        janus_publisher_id=data["janus_publisher_id"],
        display_name=data["display_name"]
    )
    return jsonify({"success": success})

if __name__ == "__main__":
    app.run()
```

### Async with asyncio

```python
import asyncio
import grpc.aio
from streambridge_pb2_grpc import StreamBridgeStub
from streambridge_pb2 import CreateSessionRequest

async def create_session_async():
    async with grpc.aio.insecure_channel("localhost:50051") as channel:
        stub = StreamBridgeStub(channel)
        request = CreateSessionRequest(
            session_id="async-room",
            output_path="async/stream.m3u8"
        )
        response = await stub.CreateSession(request)
        print(response.message)

asyncio.run(create_session_async())
```

## Error Handling

```python
import grpc

try:
    client.create_session("my-room", "output/stream.m3u8")
except grpc.RpcError as e:
    if e.code() == grpc.StatusCode.ALREADY_EXISTS:
        print("Session already exists")
    elif e.code() == grpc.StatusCode.UNAVAILABLE:
        print("Server unavailable")
    else:
        print(f"Error: {e.details()}")
```

## Troubleshooting

**Import Error**: Generated protobuf files not found
```bash
python -m grpc_tools.protoc -I../../proto --python_out=. --grpc_python_out=. ../../proto/streambridge.proto
```

**Connection Refused**: Server not running
```bash
# Start the server first
cd ../..
./bin/streambridge
```

**Module Not Found**: Dependencies not installed
```bash
pip install -r requirements.txt
```
