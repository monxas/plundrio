# Build stage
FROM --platform=$BUILDPLATFORM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build arguments for cross-compilation
ARG TARGETOS
ARG TARGETARCH

# Build the application
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags="-w -s -X main.version=docker" \
    -o plundrio \
    ./cmd/plundrio

# Final stage
FROM --platform=$TARGETPLATFORM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 plundrio && \
    adduser -D -u 1000 -G plundrio plundrio

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/plundrio /usr/local/bin/plundrio

# Create directories
RUN mkdir -p /config /downloads && \
    chown -R plundrio:plundrio /config /downloads

# Switch to non-root user
USER plundrio

# Expose port
EXPOSE 9091

# Default command
ENTRYPOINT ["plundrio"]
CMD ["run"] 