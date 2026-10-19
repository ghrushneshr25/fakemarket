package trading

import (
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// MarketDataSink receives post-durable observations for derived public MD.
// Implementations must not mutate trading state. Publish failures are best-effort
// (must not fail the trading command). Called under the instrument lock after WAL.
//
// Optional — nil sink = no live MD (legacy bootstrap / tests).
type MarketDataSink interface {
	// OnSubmitCommitted publishes Trade / BookDelta / BestQuote derived from the
	// post-match book and any new trades from this command.
	OnSubmitCommitted(
		inst domain.InstrumentID,
		asOf domain.Sequence,
		ts time.Time,
		trades []domain.Trade,
		quote QuoteSnap,
		depth BookDepthSnap,
	)
	// OnCancelCommitted publishes BookDelta / BestQuote after a durable cancel.
	OnCancelCommitted(
		inst domain.InstrumentID,
		asOf domain.Sequence,
		ts time.Time,
		quote QuoteSnap,
		depth BookDepthSnap,
	)
}
