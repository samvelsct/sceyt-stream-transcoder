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

# Build the server
build: proto
	@echo "Building streambridge server..."
	CGO_ENABLED=1 go build -o bin/streambridge ./cmd/streambridge

# Build the CLI client
cli: proto
	@echo "Building streambridge CLI..."
	go build -o bin/streambridge-cli ./cmd/streambridge-cli

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
