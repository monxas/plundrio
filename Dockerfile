# syntax=docker/dockerfile:1
ARG GO_IMAGE=golang:1.26.11-alpine3.24@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb
ARG RUNTIME_IMAGE=alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
FROM ${GO_IMAGE} AS builder
ENV GOMAXPROCS=2 GOMEMLIMIT=384MiB
WORKDIR /app
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG SOURCE_REVISION=dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM builder AS validator-builder
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -p 2 -trimpath -buildvcs=false -ldflags="-s -w" -o /app/media-validate ./cmd/media-validate

# A named context for scanner builds; it contains only the shared static CLI.
FROM scratch AS media-validator
COPY --from=validator-builder /app/media-validate /media-validate

FROM builder AS app-builder
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -p 2 -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${SOURCE_REVISION}" -o /app/plundrio ./cmd/plundrio

FROM ${RUNTIME_IMAGE} AS runtime
RUN apk add --no-cache ca-certificates tzdata ffmpeg
WORKDIR /app
COPY --from=app-builder /app/plundrio /app/plundrio
COPY --from=validator-builder /app/media-validate /usr/local/bin/media-validate
RUN mkdir -p /config && chown -R 1000:1000 /app /config
USER 1000:1000
EXPOSE 9091
ENTRYPOINT ["/app/plundrio", "run"]
