package chaos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Network fault kinds at the transport boundary (P8.4).
// These never mutate domain/WAL state directly — they only affect request delivery,
// context propagation, or response delivery to the client.
const (
	NetDropRequest      = "DROP_REQUEST"
	NetDelayRequest     = "DELAY_REQUEST"
	NetCancelRequest    = "CANCEL_REQUEST"
	NetDropResponse     = "DROP_RESPONSE"
	NetDelayResponse    = "DELAY_RESPONSE"
	NetPartialResponse  = "PARTIAL_RESPONSE" // unary: after handler, return transport error (no half-message)
	NetConnectionReset  = "CONNECTION_RESET"
	NetServerCancel     = "SERVER_CANCEL"
	NetClientDisconnect = "CLIENT_DISCONNECT" // client interceptor / caller cancel
)

// ErrNetFault is the sentinel wrapped into gRPC status for injected transport faults.
var ErrNetFault = errors.New("chaos: transport fault")

// NetworkPlan schedules one transport fault on the Nth matching unary RPC (1-based).
type NetworkPlan struct {
	Kind       string        // Net* constant
	Occurrence int           // 1-based; default 1
	Method     string        // empty = any; leaf name e.g. "SubmitOrder" or full path suffix
	Delay      time.Duration // DELAY_* only
	// Barrier: DELAY waits until ReleaseBarrier(kind) instead of sleeping (deterministic).
	Barrier bool
}

// NetworkHit records one triggered transport fault.
type NetworkHit struct {
	Kind       string
	Method     string
	Occurrence int
	At         time.Time
	Detail     string
}

// NetworkFaults is a concurrent-safe transport-boundary injector for gRPC unary RPCs.
// Production path: leave nil / never register interceptors.
type NetworkFaults struct {
	mu       sync.Mutex
	plans    []NetworkPlan
	counts   map[string]int // key = kind+"|"+methodLeaf  or kind+"|*" tracking
	rpcCount int            // all unary RPCs seen by server interceptor
	hits     []NetworkHit
	// barrier waiters: key = kind
	barriers map[string]chan struct{}
	// lastHandlerOK records whether the last DROP_RESPONSE/PARTIAL handler returned nil err
	// (financial path may have succeeded while client sees transport error).
	lastHandlerOK  bool
	lastHandlerErr error
	// Captured responses for tests (DROP_RESPONSE still ran handler).
	droppedOK int
}

// NewNetworkFaults builds an empty injector (no-op until Arm + interceptor installed).
func NewNetworkFaults() *NetworkFaults {
	return &NetworkFaults{
		counts:   make(map[string]int),
		barriers: make(map[string]chan struct{}),
	}
}

// Arm replaces plans and resets counters/hits.
func (n *NetworkFaults) Arm(plans ...NetworkPlan) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.plans = append([]NetworkPlan(nil), plans...)
	n.counts = make(map[string]int)
	n.rpcCount = 0
	n.hits = nil
	n.lastHandlerOK = false
	n.lastHandlerErr = nil
	n.droppedOK = 0
	for _, ch := range n.barriers {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	n.barriers = make(map[string]chan struct{})
}

// Hits returns a copy of triggered faults.
func (n *NetworkFaults) Hits() []NetworkHit {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]NetworkHit, len(n.hits))
	copy(out, n.hits)
	return out
}

// RPCCount is total unary RPCs observed by the server interceptor.
func (n *NetworkFaults) RPCCount() int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.rpcCount
}

// DroppedOKCount is how many DROP_RESPONSE/PARTIAL_RESPONSE ran the handler successfully
// then discarded the response (client uncertain; server may be authoritative).
func (n *NetworkFaults) DroppedOKCount() int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.droppedOK
}

// LastHandlerResult is the last DROP_RESPONSE/PARTIAL handler outcome (test helper).
func (n *NetworkFaults) LastHandlerResult() (ok bool, err error) {
	if n == nil {
		return false, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastHandlerOK, n.lastHandlerErr
}

// ReleaseBarrier unblocks DELAY_* with Barrier=true for the given kind.
func (n *NetworkFaults) ReleaseBarrier(kind string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if ch, ok := n.barriers[kind]; ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

// UnaryServerInterceptor injects faults at the gRPC server transport boundary.
// Nil receiver is a no-op interceptor (production-safe if accidentally registered).
func (n *NetworkFaults) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if n == nil {
			return handler(ctx, req)
		}
		method := methodLeaf(info.FullMethod)

		// Pre-handler faults.
		if act, plan := n.match(method, true); act != "" {
			switch act {
			case NetDropRequest:
				n.record(act, method, plan)
				return nil, status.Errorf(codes.Unavailable, "%v: %s", ErrNetFault, NetDropRequest)
			case NetConnectionReset:
				n.record(act, method, plan)
				return nil, status.Errorf(codes.Unavailable, "%v: %s", ErrNetFault, NetConnectionReset)
			case NetDelayRequest:
				n.record(act, method, plan)
				if err := n.doDelay(plan); err != nil {
					return nil, err
				}
			case NetCancelRequest, NetServerCancel:
				n.record(act, method, plan)
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				// Still invoke handler with canceled ctx — admission gates must observe cancel.
				resp, err := handler(cctx, req)
				if err == nil {
					// Handler ignored cancel (e.g. already past gates) — still drop client view?
					// For SERVER_CANCEL before work: if handler succeeds, financial state exists.
					return resp, err
				}
				return nil, mapCtxOrNet(err, act)
			case NetClientDisconnect:
				// Server-side simulation: cancel incoming context before handler.
				n.record(act, method, plan)
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				resp, err := handler(cctx, req)
				if err != nil {
					return nil, mapCtxOrNet(err, act)
				}
				return resp, nil
			}
		}

		resp, err := handler(ctx, req)

		// Post-handler faults (response path).
		if act, plan := n.match(method, false); act != "" {
			switch act {
			case NetDropResponse, NetPartialResponse:
				n.record(act, method, plan)
				n.mu.Lock()
				n.lastHandlerOK = err == nil
				n.lastHandlerErr = err
				if err == nil {
					n.droppedOK++
				}
				n.mu.Unlock()
				if err != nil {
					// Handler already failed financially — surface that, still note transport intent.
					return nil, err
				}
				// Handler succeeded (WAL may be durable) but client must not see success.
				return nil, status.Errorf(codes.Unavailable, "%v: %s", ErrNetFault, act)
			case NetDelayResponse:
				n.record(act, method, plan)
				if dErr := n.doDelay(plan); dErr != nil {
					return nil, dErr
				}
				return resp, err
			case NetConnectionReset:
				// Reset after handler: financial path may have completed.
				n.record(act, method, plan)
				n.mu.Lock()
				n.lastHandlerOK = err == nil
				n.lastHandlerErr = err
				if err == nil {
					n.droppedOK++
				}
				n.mu.Unlock()
				if err != nil {
					return nil, err
				}
				return nil, status.Errorf(codes.Unavailable, "%v: %s", ErrNetFault, NetConnectionReset)
			}
		}
		return resp, err
	}
}

// UnaryClientInterceptor injects client-side faults (disconnect/cancel before send).
func (n *NetworkFaults) UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if n == nil {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		leaf := methodLeaf(method)
		if act, plan := n.matchClient(leaf); act != "" {
			switch act {
			case NetClientDisconnect, NetCancelRequest:
				n.record(act, leaf, plan)
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				return invoker(cctx, method, req, reply, cc, opts...)
			case NetDropRequest:
				n.record(act, leaf, plan)
				return status.Errorf(codes.Unavailable, "%v: %s client", ErrNetFault, NetDropRequest)
			case NetDelayRequest:
				n.record(act, leaf, plan)
				if err := n.doDelay(plan); err != nil {
					return err
				}
			}
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// match finds a pre (request-side) or post (response-side) plan for this method.
// pre=true matches DROP_REQUEST, DELAY_REQUEST, CANCEL_*, CONNECTION_RESET (pre), CLIENT_DISCONNECT.
// pre=false matches DROP_RESPONSE, DELAY_RESPONSE, PARTIAL_RESPONSE, CONNECTION_RESET (post-armed separately).
//
// CONNECTION_RESET: first matching plan fires. If only one plan, it is treated as pre-handler
// unless Kind is explicitly used after a marker — we use Occurrence on shared counter per kind+method.
func (n *NetworkFaults) match(method string, pre bool) (kind string, plan NetworkPlan) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if pre {
		n.rpcCount++
	}
	for _, p := range n.plans {
		if !methodMatch(p.Method, method) {
			continue
		}
		side := planSide(p.Kind)
		if pre && side != sidePre && side != sideBoth {
			continue
		}
		if !pre && side != sidePost {
			continue
		}
		key := p.Kind + "|" + normalizeMethod(p.Method)
		n.counts[key]++
		occ := p.Occurrence
		if occ <= 0 {
			occ = 1
		}
		if n.counts[key] != occ {
			continue
		}
		return p.Kind, p
	}
	return "", NetworkPlan{}
}

// matchClient only client-side kinds.
func (n *NetworkFaults) matchClient(method string) (kind string, plan NetworkPlan) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range n.plans {
		if p.Kind != NetClientDisconnect && p.Kind != NetDropRequest && p.Kind != NetCancelRequest && p.Kind != NetDelayRequest {
			continue
		}
		// Client interceptor uses prefix "client|" to not collide with server counters when both installed.
		if !methodMatch(p.Method, method) {
			continue
		}
		key := "client|" + p.Kind + "|" + normalizeMethod(p.Method)
		n.counts[key]++
		occ := p.Occurrence
		if occ <= 0 {
			occ = 1
		}
		if n.counts[key] != occ {
			continue
		}
		return p.Kind, p
	}
	return "", NetworkPlan{}
}

func (n *NetworkFaults) record(kind, method string, plan NetworkPlan) {
	n.mu.Lock()
	defer n.mu.Unlock()
	occ := plan.Occurrence
	if occ <= 0 {
		occ = 1
	}
	n.hits = append(n.hits, NetworkHit{
		Kind: kind, Method: method, Occurrence: occ, At: time.Now().UTC(),
		Detail: fmt.Sprintf("kind=%s method=%s occ=%d", kind, method, occ),
	})
}

func (n *NetworkFaults) doDelay(plan NetworkPlan) error {
	if plan.Barrier {
		n.mu.Lock()
		ch, ok := n.barriers[plan.Kind]
		if !ok {
			ch = make(chan struct{})
			n.barriers[plan.Kind] = ch
		}
		n.mu.Unlock()
		<-ch
		return nil
	}
	if plan.Delay > 0 {
		time.Sleep(plan.Delay)
	}
	return nil
}

const (
	sidePre  = 1
	sidePost = 2
	sideBoth = 3
)

func planSide(kind string) int {
	switch kind {
	case NetDropRequest, NetDelayRequest, NetCancelRequest, NetServerCancel, NetClientDisconnect:
		return sidePre
	case NetDropResponse, NetDelayResponse, NetPartialResponse:
		return sidePost
	case NetConnectionReset:
		// CONNECTION_RESET defaults to pre-handler (request never reaches app).
		// For post-handler reset after durable work, arm DROP_RESPONSE or PARTIAL_RESPONSE
		// (financially equivalent: client uncertain, WAL authoritative).
		return sidePre
	default:
		return 0
	}
}

func methodLeaf(full string) string {
	if i := strings.LastIndex(full, "/"); i >= 0 && i+1 < len(full) {
		return full[i+1:]
	}
	return full
}

func normalizeMethod(m string) string {
	if m == "" {
		return "*"
	}
	return methodLeaf(m)
}

func methodMatch(filter, leaf string) bool {
	if filter == "" {
		return true
	}
	return methodLeaf(filter) == leaf
}

func mapCtxOrNet(err error, act string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled {
		return status.Errorf(codes.Canceled, "%v: %s", ErrNetFault, act)
	}
	if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded {
		return status.Errorf(codes.DeadlineExceeded, "%v: %s", ErrNetFault, act)
	}
	// Preserve application errors.
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Errorf(codes.Canceled, "%v: %s: %v", ErrNetFault, act, err)
}

// ClassifyClientError maps a client-observed error to a coarse transport view.
// It does NOT classify financial outcomes — those come from WAL/Query only.
func ClassifyClientError(err error) string {
	if err == nil {
		return "SUCCESS"
	}
	st, ok := status.FromError(err)
	if !ok {
		if errors.Is(err, context.DeadlineExceeded) {
			return "TIMEOUT"
		}
		if errors.Is(err, context.Canceled) {
			return "CANCELED"
		}
		return "TRANSPORT_ERROR"
	}
	switch st.Code() {
	case codes.OK:
		return "SUCCESS"
	case codes.DeadlineExceeded:
		return "TIMEOUT"
	case codes.Canceled:
		return "CANCELED"
	case codes.Unavailable, codes.Unknown, codes.Internal, codes.Aborted, codes.ResourceExhausted:
		return "TRANSPORT_ERROR"
	default:
		// FailedPrecondition etc. are application financial rejections, not transport uncertainty.
		return "APPLICATION_ERROR"
	}
}

// IsTransportUncertainty reports whether the client cannot infer financial success/rejection
// from the error alone (timeout, cancel, unavailable, connection loss).
func IsTransportUncertainty(err error) bool {
	v := ClassifyClientError(err)
	return v == "TIMEOUT" || v == "CANCELED" || v == "TRANSPORT_ERROR"
}
