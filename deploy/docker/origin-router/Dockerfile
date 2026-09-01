# cmd/streambridge-origin-router (Epic D) -- pure Go, no CGo, no WebRTC/FFmpeg
# native deps, unlike the main Dockerfile's streamer image. Deliberately a
# separate, minimal Dockerfile rather than a second stage in the main one:
# sharing it would still pay for the FFmpeg dev libs and libwebrtc_hls.so in
# the build stage for a binary that needs neither.
FROM golang:1.25-bookworm AS builder

ARG GITHUB_TOKEN
ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn \
    GOPRIVATE=github.com/samvelsct/* \
    GOWORK=off

RUN git config --global url."https://${GITHUB_TOKEN}:x-oauth-basic@github.com/".insteadOf "https://github.com/"

WORKDIR /build

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN go build -a -ldflags="-w -s" -o origin-router ./cmd/streambridge-origin-router

# Final stage -- no FFmpeg/webrtc runtime libs, no goofys/fuse: the Origin
# Router touches only Redis and Fleet Controller's gRPC, never HLS output or
# S3.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /root/

COPY --from=builder /build/origin-router .

EXPOSE 8090

# ENTRYPOINT (not just CMD) so Kubernetes' `args:` (set without `command:` in
# the Deployment) has an executable to append to -- with CMD alone here,
# args-only overriding replaces Cmd entirely and there's nothing left to
# exec.
ENTRYPOINT ["./origin-router"]
CMD ["--config", "/etc/vt-stream-transcoder-origin-router/config.yaml"]
