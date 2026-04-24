FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o aegis-rpc ./cmd/aegis-rpc

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/aegis-rpc /usr/local/bin/aegis-rpc

LABEL org.opencontainers.image.title="AegisRPC" \
      org.opencontainers.image.description="Smart JSON-RPC proxy with health checks, caching, and auto-failover" \
      org.opencontainers.image.source="https://github.com/ChickenBenny/AegisRPC" \
      org.opencontainers.image.licenses="MIT"

ENTRYPOINT ["aegis-rpc"]
