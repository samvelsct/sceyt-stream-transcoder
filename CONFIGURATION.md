# StreamBridge Configuration Guide

This guide explains how to configure StreamBridge using config files, environment variables, and command-line flags.

## Configuration Priority

Settings are applied in the following order (highest priority first):
1. Command-line flags
2. Environment variables
3. Configuration file
4. Default values

## Quick Start

### 1. Using Default Configuration

Run with built-in defaults:
```bash
./bin/streambridge
```

Default values:
- Server port: 50051
- HLS output: /tmp/hls
- Janus gateway: ws://localhost:8188

### 2. Using a Configuration File

Create a config file from the example:
```bash
cp config.example.yaml config.yaml
# Edit config.yaml as needed
./bin/streambridge --config config.yaml
```

### 3. Using Environment Variables

```bash
export STREAMBRIDGE_PORT=9090
export HLS_OUTPUT_DIR=/var/hls
export JANUS_GATEWAY_ADDRESS="ws://janus.example.com:8188"
./bin/streambridge
```

### 4. Combining Methods

```bash
# Load config file, override port with env var, show final config
export STREAMBRIDGE_PORT=9090
./bin/streambridge --config config.yaml --show-config
```

## Configuration Options

### Server Configuration

Controls the gRPC server behavior.

| Option | Config File | Environment Variable | Flag | Default | Description |
|--------|-------------|---------------------|------|---------|-------------|
| Port | `server.port` | `STREAMBRIDGE_PORT` | `--port` | 50051 | gRPC server port |
| Max Streams | `server.max_concurrent_streams` | `STREAMBRIDGE_MAX_STREAMS` | - | 100 | Max concurrent gRPC streams |
| Timeout | `server.connection_timeout` | `STREAMBRIDGE_TIMEOUT` | - | 30s | Connection keepalive timeout |
| Reflection | `server.enable_reflection` | `STREAMBRIDGE_REFLECTION` | - | true | Enable gRPC reflection |

**Example (YAML):**
```yaml
server:
  port: 50051
  max_concurrent_streams: 200
  connection_timeout: 60s
  enable_reflection: true
```

**Example (Environment):**
```bash
export STREAMBRIDGE_PORT=50051
export STREAMBRIDGE_MAX_STREAMS=200
export STREAMBRIDGE_TIMEOUT=60s
export STREAMBRIDGE_REFLECTION=true
```

### Janus Gateway Configuration

Default values for Janus Gateway connections. These are used when AddInput requests don't specify Janus parameters.

| Option | Config File | Environment Variable | Default | Description |
|--------|-------------|---------------------|---------|-------------|
| Gateway Address | `janus.gateway_address` | `JANUS_GATEWAY_ADDRESS` | ws://localhost:8188 | Janus WebSocket URL |
| Admin Key | `janus.admin_key` | `JANUS_ADMIN_KEY` | adminpwd | Janus admin API key |
| Admin Secret | `janus.admin_secret` | `JANUS_ADMIN_SECRET` | admin | Janus admin secret |
| Timeout | `janus.timeout` | `JANUS_TIMEOUT` | 10 | Connection timeout (seconds) |

**Example (YAML):**
```yaml
janus:
  gateway_address: "ws://janus.prod.com:8188"
  admin_key: "my-secret-key"
  admin_secret: "my-secret"
  timeout: 15
```

**Example (Environment):**
```bash
export JANUS_GATEWAY_ADDRESS="ws://janus.prod.com:8188"
export JANUS_ADMIN_KEY="my-secret-key"
export JANUS_ADMIN_SECRET="my-secret"
export JANUS_TIMEOUT=15
```

**Usage:** When calling AddInput, omit Janus parameters to use defaults:
```bash
# Uses configured defaults
grpcurl -plaintext -d '{
  "session_id": "room-123",
  "janus_room_id": 1234,
  "janus_session_id": 5678,
  "janus_handle_id": 9012,
  "janus_publisher_id": 3456,
  "display_name": "John Doe"
}' localhost:50051 streambridge.StreamBridge/AddInput
```

### HLS Output Configuration

Controls HLS stream output settings.

| Option | Config File | Environment Variable | Default | Description |
|--------|-------------|---------------------|---------|-------------|
| Output Dir | `hls.output_dir` | `HLS_OUTPUT_DIR` | /tmp/hls | Base directory for HLS files |
| Segment Duration | `hls.segment_duration` | `HLS_SEGMENT_DURATION` | 4 | Segment duration (seconds) |
| Playlist Length | `hls.playlist_length` | `HLS_PLAYLIST_LENGTH` | 5 | Segments in playlist |
| Enable GStreamer | `hls.enable_gstreamer` | `HLS_ENABLE_GSTREAMER` | false | Use GStreamer (vs FFmpeg) |

**Example (YAML):**
```yaml
hls:
  output_dir: "/var/www/hls"
  segment_duration: 6
  playlist_length: 10
  enable_gstreamer: false
```

**Example (Environment):**
```bash
export HLS_OUTPUT_DIR="/var/www/hls"
export HLS_SEGMENT_DURATION=6
export HLS_PLAYLIST_LENGTH=10
export HLS_ENABLE_GSTREAMER=false
```

**Usage:** Relative paths in CreateSession are joined with output_dir:
```bash
# Creates /var/www/hls/room-123/stream.m3u8
grpcurl -plaintext -d '{
  "session_id": "room-123",
  "output_path": "room-123/stream.m3u8"
}' localhost:50051 streambridge.StreamBridge/CreateSession
```

### Logging Configuration

Controls logging output.

| Option | Config File | Environment Variable | Default | Description |
|--------|-------------|---------------------|---------|-------------|
| Level | `logging.level` | `LOG_LEVEL` | info | debug, info, warn, error |
| Format | `logging.format` | `LOG_FORMAT` | text | text or json |
| Output | `logging.output` | `LOG_OUTPUT` | stdout | stdout, stderr, or file path |

**Example (YAML):**
```yaml
logging:
  level: "debug"
  format: "json"
  output: "/var/log/streambridge.log"
```

**Example (Environment):**
```bash
export LOG_LEVEL="debug"
export LOG_FORMAT="json"
export LOG_OUTPUT="/var/log/streambridge.log"
```

### Ownership Registry Configuration (horizontal scaling)

Disabled by default — a standalone instance behaves exactly as before this existed. See
`docs/horizontal-scaling.md` for the full design (Ownership Registry + Origin Router, and how they relate to
Fleet Controller's separate role in placing *new* sessions).

| Option | Config File | Environment Variable | Default | Description |
|--------|-------------|---------------------|---------|-------------|
| Enabled | `registry.enabled` | `REGISTRY_ENABLED` | false | Opt-in; false leaves behavior unchanged |
| Redis address | `registry.redis_addr` | `REGISTRY_REDIS_ADDR` | localhost:6379 | |
| Redis password | `registry.redis_password` | `REGISTRY_REDIS_PASSWORD` | (empty) | |
| Redis DB | `registry.redis_db` | `REGISTRY_REDIS_DB` | 0 | |
| Key prefix | `registry.key_prefix` | `REGISTRY_KEY_PREFIX` | streambridge: | |
| Worker ID | `registry.worker_id` | `REGISTRY_WORKER_ID` | $POD_NAME, else hostname | Must match Fleet Controller's discovered instance ID |
| Origin address | `registry.origin_addr` | `REGISTRY_ORIGIN_ADDR` | $POD_IP + HLS port | Set explicitly outside Kubernetes |
| Session TTL | `registry.session_ttl` | `REGISTRY_SESSION_TTL` | 24h | Coarse safety-net only, not the primary cleanup path |

**Example (Environment, Kubernetes):**
```bash
export REGISTRY_ENABLED=true
export REGISTRY_REDIS_ADDR=redis:6379
# REGISTRY_WORKER_ID and REGISTRY_ORIGIN_ADDR left unset — composed from
# $POD_NAME / $POD_IP (Downward API) automatically.
```

### Origin Router Configuration (horizontal scaling)

Only used by the separate `streambridge-origin-router` binary.

| Option | Config File | Environment Variable | Default | Description |
|--------|-------------|---------------------|---------|-------------|
| Listen address | `origin_router.listen_addr` | `ORIGIN_ROUTER_LISTEN_ADDR` | :8090 | |
| Ownership cache TTL | `origin_router.ownership_cache_ttl` | `ORIGIN_ROUTER_OWNERSHIP_CACHE_TTL` | 2s | |
| Fleet Controller enabled | `origin_router.fleet_controller.enabled` | `FLEET_CONTROLLER_ENABLED` | false | Liveness cross-check (Epic D4); works without it, just without that check |
| Fleet Controller gRPC address | `origin_router.fleet_controller.grpc_addr` | `FLEET_CONTROLLER_GRPC_ADDR` | (empty) | |
| Fleet Controller service name | `origin_router.fleet_controller.service_name` | `FLEET_CONTROLLER_SERVICE_NAME` | streambridge | Must match the fleet name Fleet Controller registers StreamBridge under |

## Command-Line Flags

Available flags:

| Flag | Description | Example |
|------|-------------|---------|
| `--config` | Path to YAML config file | `--config /etc/streambridge.yaml` |
| `--port` | Override server port | `--port 9090` |
| `--show-config` | Show current config and exit | `--show-config` |

## Common Scenarios

### Production Deployment

```yaml
# /etc/streambridge/config.yaml
server:
  port: 50051
  max_concurrent_streams: 500
  connection_timeout: 120s

janus:
  gateway_address: "ws://janus.internal:8188"
  admin_key: "${JANUS_KEY}"  # Use secrets from env
  admin_secret: "${JANUS_SECRET}"

hls:
  output_dir: "/var/www/hls"
  segment_duration: 4
  playlist_length: 10

logging:
  level: "info"
  format: "json"
  output: "/var/log/streambridge/server.log"
```

```bash
export JANUS_KEY="prod-key"
export JANUS_SECRET="prod-secret"
./bin/streambridge --config /etc/streambridge/config.yaml
```

### Development Setup

```bash
# Use defaults with debug logging
export LOG_LEVEL="debug"
export HLS_OUTPUT_DIR="./output"
./bin/streambridge
```

### Multiple Environments

```bash
# Development
./bin/streambridge --config config.dev.yaml

# Staging
./bin/streambridge --config config.staging.yaml

# Production
./bin/streambridge --config config.prod.yaml
```

## Validation

The configuration is validated on startup. Common errors:

- **Invalid port**: Port must be between 1-65535
- **Missing HLS directory**: output_dir is required
- **Invalid log level**: Must be debug, info, warn, or error
- **Invalid duration**: Segment duration must be >= 1

Check your configuration:
```bash
./bin/streambridge --config config.yaml --show-config
```

## Tips

1. **Use environment variables for secrets**: Don't commit sensitive data (API keys, secrets) to config files
2. **Relative paths**: Use relative output paths in CreateSession for easier organization
3. **Defaults**: Configure Janus defaults to simplify AddInput requests
4. **Validation**: Always run with `--show-config` first to verify settings
5. **Monitoring**: Use JSON logging format for production log aggregation

## Troubleshooting

**Config file not found**: File path is relative to current directory
```bash
./bin/streambridge --config /absolute/path/to/config.yaml
```

**Environment variables not working**: Check variable names (case-sensitive)
```bash
env | grep STREAMBRIDGE
env | grep JANUS
env | grep HLS
env | grep LOG
```

**Settings not applying**: Check priority order (flags > env > config > defaults)
```bash
./bin/streambridge --config config.yaml --show-config
```
