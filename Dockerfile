# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install git for version info
RUN apk add --no-cache git

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o plundrio ./cmd/plundrio

# Final stage
FROM alpine:latest

# Add ca-certificates for HTTPS
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy the binary
COPY --from=builder /app/plundrio /app/plundrio

# Create config directory
RUN mkdir -p /config

EXPOSE 9091

ENTRYPOINT ["/app/plundrio"]
