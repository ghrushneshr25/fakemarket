# P10.1 multi-stage production image — no secrets, non-root runtime.
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fakemarket ./cmd/fakemarket

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget \
	&& addgroup -S fakemarket && adduser -S -G fakemarket fakemarket \
	&& mkdir -p /data/wal /data/consensus \
	&& chown -R fakemarket:fakemarket /data
WORKDIR /app
COPY --from=builder /out/fakemarket /app/fakemarket
USER fakemarket
ENV FAKEMARKET_ENV=production \
	FAKEMARKET_HTTP_PORT=8080 \
	FAKEMARKET_DATA_DIR=/data \
	FAKEMARKET_WAL_DIR=/data/wal \
	FAKEMARKET_CONSENSUS_DIR=/data/consensus
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
	CMD wget -qO- http://127.0.0.1:8080/ready || exit 1
ENTRYPOINT ["/app/fakemarket"]
