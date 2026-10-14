package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEnginePhaseAndTransportMetrics(t *testing.T) {
	m := New()
	m.ObserveEnginePhase(PhaseExecutorWait, "submit", 5*time.Microsecond)
	m.ObserveEnginePhase(PhaseRisk, "submit", 10*time.Microsecond)
	m.ObserveEnginePhase(PhaseMatching, "submit", 20*time.Microsecond)
	m.ObserveEnginePhase(PhaseJournal, "submit", 100*time.Microsecond)
	m.ObserveTransport("grpc", "SubmitOrder", 200*time.Microsecond)
	m.ObserveTrading("submit", ResultOK, 150*time.Microsecond)

	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rr.Body)
	s := string(body)
	for _, want := range []string{
		"fakemarket_engine_phase_duration_seconds",
		"phase=\"executor_wait\"",
		"phase=\"risk\"",
		"phase=\"matching\"",
		"phase=\"journal\"",
		"fakemarket_grpc_server_rpc_duration_seconds",
		"transport=\"grpc\"",
		"fakemarket_trading_command_duration_seconds",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestLatencyDocsPresent(t *testing.T) {
	if DocTradingDuration == "" || DocEnginePhase == "" || DocWALAppend == "" {
		t.Fatal("docs")
	}
}
