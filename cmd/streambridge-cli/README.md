# StreamBridge CLI

Command-line interface for controlling StreamBridge server.

## Installation

```bash
# Build from source
cd cmd/streambridge-cli
go build -o streambridge-cli

# Or use Make from project root
cd ../..
make cli
```

## Usage

```bash
streambridge-cli [options] <command> [args...]
```

### Options

- `--server <address>` - Server address (default: localhost:50051)
- `--timeout <duration>` - Request timeout (default: 10s)

### Commands

#### Session Management

**Create a session:**
```bash
streambridge-cli session create <session-id> <output-path> [--gst]
```

Examples:
```bash
# Create session with default FFmpeg backend
streambridge-cli session create room-123 output/stream.m3u8

# Create session with GStreamer backend
streambridge-cli session create room-456 output/stream.m3u8 --gst

# Relative path (joined with server's HLS output dir)
streambridge-cli session create room-789 room-789/stream.m3u8
```

**Destroy a session:**
```bash
streambridge-cli session destroy <session-id>
```

Example:
```bash
streambridge-cli session destroy room-123
```

**Get session info:**
```bash
streambridge-cli session info <session-id>
```

Example:
```bash
streambridge-cli session info room-123
```

Output:
```
Session: room-123
Participants: 2
  1. Alice
  2. Bob
```

#### Input Management

**Add WebRTC input:**
```bash
streambridge-cli input add <session-id> \
  --room <janus-room-id> \
  --session <janus-session-id> \
  --handle <janus-handle-id> \
  --publisher <janus-publisher-id> \
  --name <display-name> \
  [--gateway <address>] \
  [--key <admin-key>] \
  [--secret <admin-secret>]
```

Example:
```bash
# With server defaults (gateway, key, secret)
streambridge-cli input add room-123 \
  --room 1234 \
  --session 5678 \
  --handle 9012 \
  --publisher 3456 \
  --name "Alice"

# With custom Janus connection
streambridge-cli input add room-123 \
  --room 1234 \
  --session 5678 \
  --handle 9012 \
  --publisher 3456 \
  --name "Bob" \
  --gateway "ws://janus.example.com:8188" \
  --key "my-key" \
  --secret "my-secret"
```

**Remove WebRTC input:**
```bash
streambridge-cli input remove <session-id> <janus-session-id> <janus-handle-id> <display-name>
```

Example:
```bash
streambridge-cli input remove room-123 5678 9012 "Alice"
```

#### Participant Status

**Set mute status:**
```bash
streambridge-cli mute <session-id> <user-id> <client-id> <true|false>
```

Examples:
```bash
# Mute user
streambridge-cli mute room-123 user-1 client-1 true

# Unmute user
streambridge-cli mute room-123 user-1 client-1 false
```

**Set video status:**
```bash
streambridge-cli video <session-id> <user-id> <client-id> <true|false>
```

Examples:
```bash
# Turn video off
streambridge-cli video room-123 user-1 client-1 false

# Turn video on
streambridge-cli video room-123 user-1 client-1 true
```

#### Metadata

**Write ID3 tag:**
```bash
streambridge-cli tag <session-id> <event-data> <event-type>
```

Examples:
```bash
# Simple event
streambridge-cli tag room-123 '{"event":"hand-raised"}' "custom"

# Detailed event
streambridge-cli tag room-123 \
  '{"event":"participant-joined","userId":"user-123","name":"Alice","timestamp":"2024-02-19T10:30:00Z"}' \
  "participant-event"
```

## Complete Workflow Example

```bash
# 1. Create a session
streambridge-cli session create conference-room output/conference.m3u8

# 2. Add first participant
streambridge-cli input add conference-room \
  --room 1001 \
  --session 2001 \
  --handle 3001 \
  --publisher 4001 \
  --name "Alice"

# 3. Add second participant
streambridge-cli input add conference-room \
  --room 1001 \
  --session 2002 \
  --handle 3002 \
  --publisher 4002 \
  --name "Bob"

# 4. Check participants
streambridge-cli session info conference-room

# 5. Mute Alice
streambridge-cli mute conference-room alice-user alice-client true

# 6. Turn off Bob's video
streambridge-cli video conference-room bob-user bob-client false

# 7. Send custom event
streambridge-cli tag conference-room '{"event":"recording-started"}' "system"

# 8. Remove Bob
streambridge-cli input remove conference-room 2002 3002 "Bob"

# 9. Destroy session when done
streambridge-cli session destroy conference-room
```

## Shell Scripts

Use in bash scripts for automation:

```bash
#!/bin/bash

# Create session
if streambridge-cli session create my-room output/stream.m3u8; then
    echo "Session created"
else
    echo "Failed to create session"
    exit 1
fi

# Add multiple participants
participants=(
    "1234:5678:9012:3456:Alice"
    "1234:5679:9013:3457:Bob"
    "1234:5680:9014:3458:Charlie"
)

for participant in "${participants[@]}"; do
    IFS=':' read -r room session handle publisher name <<< "$participant"
    streambridge-cli input add my-room \
        --room "$room" \
        --session "$session" \
        --handle "$handle" \
        --publisher "$publisher" \
        --name "$name"
done

# Wait
sleep 60

# Clean up
streambridge-cli session destroy my-room
```

## Remote Server

Connect to remote StreamBridge server:

```bash
# Production server
streambridge-cli --server prod.example.com:50051 session info room-123

# Development server with custom timeout
streambridge-cli --server dev.example.com:9090 --timeout 30s session create test-room output.m3u8
```

## Error Handling

The CLI exits with:
- **0** - Success
- **1** - Error occurred

Check exit code in scripts:

```bash
if streambridge-cli session create my-room output.m3u8; then
    echo "Success"
else
    echo "Failed with exit code: $?"
fi
```

## Output Format

Success messages start with ✓:
```
✓ session created successfully
✓ input added successfully
✓ session destroyed successfully
```

Errors go to stderr:
```
Error: session not found
Error: rpc error: code = NotFound desc = session not found
```

## Tips

1. **Use relative paths** for output to benefit from server's HLS output dir config
2. **Omit Janus params** when using server defaults for cleaner commands
3. **Quote JSON** event data to avoid shell interpretation
4. **Check session info** regularly to monitor participants
5. **Script repetitive tasks** for production workflows

## Troubleshooting

**Connection refused:**
```bash
# Check if server is running
streambridge-cli --server localhost:50051 session info test
```

**Timeout:**
```bash
# Increase timeout for slow networks
streambridge-cli --timeout 30s session create room-123 output.m3u8
```

**Invalid arguments:**
```bash
# Show help
streambridge-cli --help
streambridge-cli session --help
streambridge-cli input --help
```
