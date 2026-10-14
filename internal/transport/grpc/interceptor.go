package grpcapi

import (
	"context"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"google.golang.org/grpc"
)

// UnaryMetricsInterceptor records transport-level unary RPC latency (P7.2).
//
// Boundary: server interceptor entry → handler return (includes codec decode/encode
// on the server and the full Service call). This is NOT pure engine latency and
// is NOT client RTT (client-side would need a separate interceptor).
func UnaryMetricsInterceptor(m *metrics.Registry) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if m == nil {
			return handler(ctx, req)
		}
		start := time.Now()
		resp, err := handler(ctx, req)
		op := info.FullMethod
		// Bound operation label to method leaf when possible.
		if i := lastSlash(op); i >= 0 && i+1 < len(op) {
			op = op[i+1:]
		}
		m.ObserveTransport("grpc", op, time.Since(start))
		return resp, err
	}
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
