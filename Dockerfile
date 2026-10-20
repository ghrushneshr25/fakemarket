# P10.1 multi-stage production image — no secrets, non-root runtime.
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fakemarket ./cmd/fakemarket \
	&& CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fakemarket-sim ./cmd/fakemarket-sim \
	&& CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fakemarket-scale ./cmd/fakemarket-scale \
	&& CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fakemarket-demo ./cmd/fakemarket-demo

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget \
	&& addgroup -S fakemarket && adduser -S -G fakemarket fakemarket \
	&& mkdir -p /data/wal /data/consensus \
	&& chown -R fakemarket:fakemarket /data
WORKDIR /app
COPY --from=builder /out/fakemarket /app/fakemarket
COPY --from=builder /out/fakemarket-sim /app/fakemarket-sim
COPY --from=builder /out/fakemarket-scale /app/fakemarket-scale
COPY --from=builder /out/fakemarket-demo /app/fakemarket-demo
USER fakemarket
ENV FAKEMARKET_ENV=production \
	FAKEMARKET_HTTP_PORT=8080 \
	FAKEMARKET_DATA_DIR=/data \
	FAKEMARKET_WAL_DIR=/data/wal \
	FAKEMARKET_CONSENSUS_DIR=/data/consensus
EXPOSE 8080 9100 50051
VOLUME ["/data"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
	CMD wget -qO- http://127.0.0.1:8080/ready || exit 1
ENTRYPOINT ["/app/fakemarket"]
