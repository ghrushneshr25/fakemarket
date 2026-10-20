package recovery

import (
	"fmt"
	"sort"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// State is exchange state reconstructed from the WAL (empty → fold).
type State struct {
	Instruments  *domain.InstrumentRegistry
	Accounts     map[domain.AccountID]*domain.Account
	Orders       map[domain.OrderID]*domain.Order
	Books        map[domain.InstrumentID]*book.OrderBook
	Reservations *domain.ReservationStore
	Trades       []domain.Trade
	Executions   []domain.Execution

	LastEventSequence domain.Sequence
	EventsApplied     uint64
	seenEventIDs      map[string]struct{}
	seenTradeIDs      map[string]struct{}
}

// NewState builds empty recovery state with seeded instruments and accounts.
func NewState(instruments *domain.InstrumentRegistry, accounts map[domain.AccountID]*domain.Account) *State {
	if instruments == nil {
		instruments = domain.NewInstrumentRegistry()
	}
	if accounts == nil {
		accounts = make(map[domain.AccountID]*domain.Account)
	}
	return &State{
		Instruments:  instruments,
		Accounts:     accounts,
		Orders:       make(map[domain.OrderID]*domain.Order),
		Books:        make(map[domain.InstrumentID]*book.OrderBook),
		Reservations: domain.NewReservationStore(),
		seenEventIDs: make(map[string]struct{}),
		seenTradeIDs: make(map[string]struct{}),
	}
}

func (s *State) bookFor(id domain.InstrumentID) (*book.OrderBook, error) {
	if b, ok := s.Books[id]; ok {
		return b, nil
	}
	b, err := book.NewOrderBook(id)
	if err != nil {
		return nil, err
	}
	s.Books[id] = b
	return b, nil
}

// Canonical is a deterministic snapshot for equality tests.
type Canonical struct {
	LastSequence  domain.Sequence
	EventsApplied uint64
	Orders        []OrderSnap
	Trades        []TradeSnap
	Accounts      []AccountSnap
	Reservations  []ResSnap
	BidLevels     []LevelSnap
	AskLevels     []LevelSnap
}

type OrderSnap struct {
	ID, Account, Instrument string
	Side, Type, Status      string
	Qty, Remaining          int64
	Price                   int64 // -1 if none
	Sequence                uint64
}

type TradeSnap struct {
	ID, Instrument, Buy, Sell string
	Price, Qty                int64
	Sequence                  uint64
	// TimestampUnixNano is trade observation time (not matching priority). 0 = unknown/legacy.
	TimestampUnixNano int64 `json:"ts_unix_nano,omitempty"`
}

type AccountSnap struct {
	ID        string
	Cash      int64
	Positions []PosSnap
}

type PosSnap struct {
	Instrument string
	Qty        int64
}

type ResSnap struct {
	OrderID          string
	Account          string
	Instrument       string
	Cash             int64
	PositionQuantity int64
}

type LevelSnap struct {
	Instrument string
	Price      int64
	OrderIDs   []string
	Qtys       []int64
}

// Snapshot builds a deterministic canonical view.
func (s *State) Snapshot() Canonical {
	c := Canonical{
		LastSequence:  s.LastEventSequence,
		EventsApplied: s.EventsApplied,
	}
	oids := make([]string, 0, len(s.Orders))
	for id := range s.Orders {
		oids = append(oids, string(id))
	}
	sort.Strings(oids)
	for _, id := range oids {
		o := s.Orders[domain.OrderID(id)]
		ps := int64(-1)
		if p, ok := o.Price(); ok {
			ps = int64(p)
		}
		c.Orders = append(c.Orders, OrderSnap{
			ID: id, Account: string(o.AccountID), Instrument: string(o.InstrumentID),
			Side: string(o.Side), Type: string(o.Type), Status: string(o.Status()),
			Qty: int64(o.Quantity), Remaining: int64(o.RemainingQuantity()),
			Price: ps, Sequence: uint64(o.Sequence),
		})
	}
	for _, t := range s.Trades {
		ts := int64(0)
		if !t.Timestamp.IsZero() {
			ts = t.Timestamp.UTC().UnixNano()
		}
		c.Trades = append(c.Trades, TradeSnap{
			ID: string(t.ID), Instrument: string(t.InstrumentID),
			Buy: string(t.BuyOrderID), Sell: string(t.SellOrderID),
			Price: int64(t.Price), Qty: int64(t.Quantity), Sequence: uint64(t.Sequence),
			TimestampUnixNano: ts,
		})
	}
	aids := make([]string, 0, len(s.Accounts))
	for id := range s.Accounts {
		aids = append(aids, string(id))
	}
	sort.Strings(aids)
	for _, id := range aids {
		a := s.Accounts[domain.AccountID(id)]
		as := AccountSnap{ID: id, Cash: int64(a.Cash())}
		for _, p := range a.Positions() {
			as.Positions = append(as.Positions, PosSnap{Instrument: string(p.InstrumentID), Qty: int64(p.Quantity)})
		}
		sort.Slice(as.Positions, func(i, j int) bool {
			return as.Positions[i].Instrument < as.Positions[j].Instrument
		})
		c.Accounts = append(c.Accounts, as)
	}
	for _, id := range oids {
		if r, ok := s.Reservations.Get(domain.OrderID(id)); ok {
			c.Reservations = append(c.Reservations, ResSnap{
				OrderID: id, Account: string(r.AccountID), Instrument: string(r.InstrumentID),
				Cash: int64(r.Cash), PositionQuantity: int64(r.PositionQuantity),
			})
		}
	}
	iids := make([]string, 0, len(s.Books))
	for id := range s.Books {
		iids = append(iids, string(id))
	}
	sort.Strings(iids)
	for _, iid := range iids {
		ob := s.Books[domain.InstrumentID(iid)]
		for _, px := range ob.Bids().Prices() {
			level, _ := ob.Bids().GetLevel(px)
			ls := LevelSnap{Instrument: iid, Price: int64(px)}
			for _, oid := range level.OrderIDs() {
				ls.OrderIDs = append(ls.OrderIDs, string(oid))
				if ord, ok := level.Get(oid); ok {
					ls.Qtys = append(ls.Qtys, int64(ord.RemainingQuantity()))
				}
			}
			c.BidLevels = append(c.BidLevels, ls)
		}
		for _, px := range ob.Asks().Prices() {
			level, _ := ob.Asks().GetLevel(px)
			ls := LevelSnap{Instrument: iid, Price: int64(px)}
			for _, oid := range level.OrderIDs() {
				ls.OrderIDs = append(ls.OrderIDs, string(oid))
				if ord, ok := level.Get(oid); ok {
					ls.Qtys = append(ls.Qtys, int64(ord.RemainingQuantity()))
				}
			}
			c.AskLevels = append(c.AskLevels, ls)
		}
	}
	return c
}

// CompareCanonical returns nil if equal.
// Trade observation timestamps are ignored (not matching priority / financial identity).
func CompareCanonical(a, b Canonical) error {
	if a.LastSequence != b.LastSequence || a.EventsApplied != b.EventsApplied {
		return fmt.Errorf("%w: meta seq/applied", ErrRecoveryFailed)
	}
	if fmt.Sprint(a.Orders) != fmt.Sprint(b.Orders) {
		return fmt.Errorf("%w: orders", ErrRecoveryFailed)
	}
	if !tradesFinanciallyEqual(a.Trades, b.Trades) {
		return fmt.Errorf("%w: trades", ErrRecoveryFailed)
	}
	if fmt.Sprint(a.Accounts) != fmt.Sprint(b.Accounts) {
		return fmt.Errorf("%w: accounts", ErrRecoveryFailed)
	}
	if fmt.Sprint(a.Reservations) != fmt.Sprint(b.Reservations) {
		return fmt.Errorf("%w: reservations", ErrRecoveryFailed)
	}
	if fmt.Sprint(a.BidLevels) != fmt.Sprint(b.BidLevels) {
		return fmt.Errorf("%w: bids", ErrRecoveryFailed)
	}
	if fmt.Sprint(a.AskLevels) != fmt.Sprint(b.AskLevels) {
		return fmt.Errorf("%w: asks", ErrRecoveryFailed)
	}
	return nil
}

func tradesFinanciallyEqual(a, b []TradeSnap) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.ID != y.ID || x.Instrument != y.Instrument || x.Buy != y.Buy || x.Sell != y.Sell ||
			x.Price != y.Price || x.Qty != y.Qty || x.Sequence != y.Sequence {
			return false
		}
	}
	return true
}
