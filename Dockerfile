# Use a multi-platform builder stage
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

# Set target OS and ARCH for cross-compilation
ARG TARGETOS=linux
ARG TARGETARCH

# Set working directory
WORKDIR /workspace

# Copy go mod files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the binary with cross-compilation support
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o kube-vip-controller main.go

# Use scratch for the runtime image (suitable for Linux; may not run on Windows/Darwin but can be built for binary extraction)
FROM scratch

# Copy the built binary
COPY --from=builder /workspace/kube-vip-controller /

# Set entrypoint
ENTRYPOINT ["/kube-vip-controller"]