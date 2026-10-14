package tracking

import (
	"log/slog"
)

// Logger is optional; nil-safe helpers no-op when log is nil.
type Logger = *slog.Logger

// Discard returns a logger that drops all records (tests).
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// Attr helpers keep field names consistent at call sites.
func ARequestID(v string) slog.Attr      { return slog.String(RequestID, v) }
func AIdempotencyKey(v string) slog.Attr { return slog.String(IdempotencyKey, v) }
func AOrderID(v string) slog.Attr        { return slog.String(OrderID, v) }
func AOrderSeq(v uint64) slog.Attr       { return slog.Uint64(OrderSequence, v) }
func AAccountID(v string) slog.Attr      { return slog.String(AccountID, v) }
func AInstrumentID(v string) slog.Attr   { return slog.String(InstrumentID, v) }
func AEventID(v string) slog.Attr        { return slog.String(EventID, v) }
func AEventSeq(v uint64) slog.Attr       { return slog.Uint64(EventSequence, v) }
func ATradeID(v string) slog.Attr        { return slog.String(TradeID, v) }
func AOperation(v string) slog.Attr      { return slog.String(Operation, v) }
func AOutcome(v string) slog.Attr        { return slog.String(Outcome, v) }
func ASide(v string) slog.Attr           { return slog.String(Side, v) }
func AOrderType(v string) slog.Attr      { return slog.String(OrderType, v) }
func AStatus(v string) slog.Attr         { return slog.String(Status, v) }
func AError(err error) slog.Attr {
	if err == nil {
		return slog.String(Error, "")
	}
	return slog.String(Error, err.Error())
}
