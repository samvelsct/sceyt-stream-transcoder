.PHONY: proto build cli examples run clean help

# Default target
all: proto build cli

# Generate protobuf files
proto:
	@echo "Generating protobuf files..."
	@mkdir -p api
	protoc --go_out=api --go_opt=paths=source_relative \
		--go-grpc_out=api --go-grpc_opt=paths=source_relative \
		--proto_path=proto \
		streambridge.proto

# Regenerate the local client stub for Fleet Controller's FleetStatusService.
# proto/fleetcontroller.proto is a copy of vt-fleet-controller's
# api/proto/fleetcontroller/v1/fleet_status.proto with go_package rewritten —
# Fleet Controller's generated code lives under its own internal/ package and
# can't be imported cross-module, so we generate our own client from the same
# wire contract instead. Re-copy+run this if the upstream .proto changes.
fleetproto:
	@echo "Generating Fleet Controller client stub..."
	@mkdir -p api/fleetpb
	protoc --go_out=api/fleetpb --go_opt=paths=source_relative \
		--go-grpc_out=api/fleetpb --go-grpc_opt=paths=source_relative \
		--proto_path=proto \
		fleetcontroller.proto

# Build the server
build: proto
	@echo "Building streambridge server..."
	CGO_ENABLED=1 go build -o bin/streambridge ./cmd/streambridge

# Build the CLI client
cli: proto
	@echo "Building streambridge CLI..."
	go build -o bin/streambridge-cli ./cmd/streambridge-cli

# Build the Origin Router (Epic D) -- pure Go, no CGo, unlike the main
# server build above.
origin-router:
	@echo "Building streambridge-origin-router..."
	CGO_ENABLED=0 go build -o bin/streambridge-origin-router ./cmd/streambridge-origin-router

# Build the Origin Router's Docker image (separate, lighter Dockerfile --
# see deploy/docker/origin-router/Dockerfile for why it isn't a stage in the
# main one).
docker-origin-router:
	@echo "Building streambridge-origin-router Docker image..."
	docker build -f deploy/docker/origin-router/Dockerfile -t streambridge-origin-router:latest .

# Build example clients
examples: proto
	@echo "Building Go example client..."
	cd examples/go-client && go build -o ../../bin/go-client

# Run the server
run: build
	@echo "Starting streambridge server..."
	./bin/streambridge

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -rf bin/
	rm -rf api/*.pb.go

# Show help
help:
	@echo "StreamBridge - gRPC controlled WebRTC to HLS converter"
	@echo ""
	@echo "Available targets:"
	@echo "  make proto     - Generate protobuf Go files"
	@echo "  make build     - Build the server (includes proto generation)"
	@echo "  make cli       - Build the CLI client"
	@echo "  make origin-router - Build the Origin Router (Epic D, pure Go)"
	@echo "  make docker-origin-router - Build the Origin Router's Docker image"
	@echo "  make examples  - Build example clients"
	@echo "  make all       - Build server and CLI"
	@echo "  make run       - Build and run the server"
	@echo "  make clean     - Remove build artifacts"
	@echo "  make help      - Show this help message"
	@echo ""
	@echo "Usage:"
	@echo "  make all                          # Build everything"
	@echo "  ./bin/streambridge --port 50051   # Start server"
	@echo "  ./bin/streambridge-cli session create room-123 output.m3u8"
	@echo "  ./bin/go-client --server localhost:50051"
