# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

ARG VERSION=dev

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 \
    go build -ldflags="-s -w -X github.com/viperadnan-git/opendebrid/cmd.version=${VERSION}" \
    -o /usr/local/bin/opendebrid .

# ── Runtime stage ────────────────────────────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache \
    aria2 \
    py3-pip \
    ffmpeg \
    rclone \
    ca-certificates \
    tini \
    && pip3 install --no-cache-dir --break-system-packages yt-dlp \
    && mkdir -p /data/downloads

COPY --from=builder /usr/local/bin/opendebrid /usr/local/bin/opendebrid

# HTTP + gRPC (multiplexed)
EXPOSE 8000

VOLUME ["/data/downloads"]

ENTRYPOINT ["tini", "--", "opendebrid"]
CMD ["controller"]
