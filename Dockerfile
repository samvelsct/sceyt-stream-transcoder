# Build stage
FROM ubuntu:25.04 AS builder

# Set necessary environmet variables needed for our image
ARG GITHUB_TOKEN
ENV GO111MODULE=on \
    CGO_ENABLED=1 \
    GOOS=linux \
    GOARCH=amd64 \
    GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn \
    GOPRIVATE=github.com/samvelsct/* \
    GITHUB_TOKEN=$GITHUB_TOKEN \
    DEBIAN_FRONTEND=noninteractive

# Install build dependencies including Go and FFmpeg libraries
RUN apt-get update && apt-get install -y \
    golang-1.23 \
    git \
    ca-certificates \
    gcc \
    g++ \
    libavfilter-dev \
    libavformat-dev \
    libavcodec-dev \
    libswresample-dev \
    libswscale-dev \
    libavutil-dev \
    libcurl4-gnutls-dev \
    && rm -rf /var/lib/apt/lists/*

# Add Go to PATH
ENV PATH=/usr/lib/go-1.23/bin:$PATH

RUN git config --global url."https://${GITHUB_TOKEN}:x-oauth-basic@github.com/".insteadOf "https://github.com/"

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy shared library for build
COPY ./libwebrtc_hls.so /usr/local/lib/
RUN ldconfig

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -a -ldflags="-w -s" -o app ./cmd/streambridge

# Final stage
FROM ubuntu:25.04

# Install runtime dependencies including FFmpeg libraries
RUN apt-get update && apt-get install -y \
    ca-certificates \
    libavfilter10 \
    libavformat61 \
    libavcodec61 \
    libswresample5 \
    libswscale8 \
    libavutil59 \
    libcurl3t64-gnutls \
    wget \
    fuse \
    && rm -rf /var/lib/apt/lists/*

# Install goofys
RUN wget -q https://github.com/kahing/goofys/releases/latest/download/goofys -O /usr/local/bin/goofys \
    && chmod +x /usr/local/bin/goofys

WORKDIR /root/

# Copy the binary from builder
COPY --from=builder /build/app .

# Copy shared library
COPY ./libwebrtc_hls.so /usr/local/lib/
RUN ldconfig

# Copy config file
COPY ./config.example.yaml ./config.yaml

# Copy entrypoint script
COPY ./entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

# Expose port (adjust as needed)
EXPOSE 8080

# Run the application
CMD ["./entrypoint.sh"]
