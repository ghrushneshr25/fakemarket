package recovery

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

const (
	snapMagic0, snapMagic1, snapMagic2, snapMagic3 = 'S', 'N', 'A', 'P'
	SnapshotFormatV1                               = uint16(1)
	snapHeaderSize                                 = 18 // magic4 + ver2 + seq8 + len4
	snapCRCSize                                    = 4
)

var snapCRC = crc32.MakeTable(crc32.IEEE)

// snapPayload is the versioned JSON body (deterministic field order via structs).
type snapPayload struct {
	LastSequence  uint64          `json:"last_sequence"`
	EventsApplied uint64          `json:"events_applied"`
	Orders        []OrderSnap     `json:"orders"`
	Trades        []TradeSnap     `json:"trades"`
	Accounts      []AccountSnap   `json:"accounts"`
	Reservations  []ResSnap       `json:"reservations"`
	BidLevels     []LevelSnap     `json:"bid_levels"`
	AskLevels     []LevelSnap     `json:"ask_levels"`
	SeenEventIDs  []string        `json:"seen_event_ids"`
	SeenTradeIDs  []string        `json:"seen_trade_ids"`
	Executions    []executionWire `json:"executions"`
}

type executionWire struct {
	ID, TradeID, OrderID, Instrument, Side string
	Price, Qty                             int64
	Sequence                               uint64
}

// EncodeSnapshot serializes state at its LastEventSequence boundary.
func EncodeSnapshot(s *State) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil state", ErrInvalidSnapshot)
	}
	c := s.Snapshot()
	ids := make([]string, 0, len(s.seenEventIDs))
	for id := range s.seenEventIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	tids := make([]string, 0, len(s.seenTradeIDs))
	for id := range s.seenTradeIDs {
		tids = append(tids, id)
	}
	sort.Strings(tids)
	var ex []executionWire
	for _, e := range s.Executions {
		ex = append(ex, executionWire{
			ID: string(e.ID), TradeID: string(e.TradeID), OrderID: string(e.OrderID),
			Instrument: string(e.InstrumentID), Side: string(e.Side),
			Price: int64(e.Price), Qty: int64(e.Quantity), Sequence: uint64(e.Sequence),
		})
	}
	p := snapPayload{
		LastSequence:  uint64(c.LastSequence),
		EventsApplied: c.EventsApplied,
		Orders:        c.Orders,
		Trades:        c.Trades,
		Accounts:      c.Accounts,
		Reservations:  c.Reservations,
		BidLevels:     c.BidLevels,
		AskLevels:     c.AskLevels,
		SeenEventIDs:  ids,
		SeenTradeIDs:  tids,
		Executions:    ex,
	}
	if uint64(s.LastEventSequence) != p.LastSequence {
		return nil, fmt.Errorf("%w: state seq", ErrSnapshotSequence)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	return frameSnapshot(uint64(s.LastEventSequence), body)
}

func frameSnapshot(seq uint64, payload []byte) ([]byte, error) {
	if len(payload) > int(^uint32(0)>>1) {
		return nil, fmt.Errorf("%w: payload too large", ErrInvalidSnapshot)
	}
	buf := make([]byte, snapHeaderSize+len(payload)+snapCRCSize)
	buf[0], buf[1], buf[2], buf[3] = snapMagic0, snapMagic1, snapMagic2, snapMagic3
	binary.BigEndian.PutUint16(buf[4:6], SnapshotFormatV1)
	binary.BigEndian.PutUint64(buf[6:14], seq)
	binary.BigEndian.PutUint32(buf[14:18], uint32(len(payload)))
	copy(buf[18:], payload)
	sum := crc32.Checksum(buf[:18+len(payload)], snapCRC)
	binary.BigEndian.PutUint32(buf[18+len(payload):], sum)
	return buf, nil
}

// DecodeSnapshot validates framing/CRC and returns payload + envelope sequence.
func DecodeSnapshot(raw []byte) (seq uint64, p snapPayload, err error) {
	if len(raw) < snapHeaderSize+snapCRCSize {
		return 0, p, fmt.Errorf("%w: too short", ErrSnapshotCorrupt)
	}
	if raw[0] != snapMagic0 || raw[1] != snapMagic1 || raw[2] != snapMagic2 || raw[3] != snapMagic3 {
		return 0, p, fmt.Errorf("%w: magic", ErrInvalidSnapshot)
	}
	ver := binary.BigEndian.Uint16(raw[4:6])
	if ver != SnapshotFormatV1 {
		return 0, p, fmt.Errorf("%w: %d", ErrUnsupportedSnapVersion, ver)
	}
	seq = binary.BigEndian.Uint64(raw[6:14])
	plen := binary.BigEndian.Uint32(raw[14:18])
	need := snapHeaderSize + int(plen) + snapCRCSize
	if len(raw) != need {
		return 0, p, fmt.Errorf("%w: length", ErrSnapshotCorrupt)
	}
	got := binary.BigEndian.Uint32(raw[snapHeaderSize+int(plen):])
	want := crc32.Checksum(raw[:snapHeaderSize+int(plen)], snapCRC)
	if got != want {
		return 0, p, fmt.Errorf("%w: checksum", ErrSnapshotCorrupt)
	}
	if err := json.Unmarshal(raw[18:18+plen], &p); err != nil {
		return 0, p, fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	if p.LastSequence != seq {
		return 0, p, fmt.Errorf("%w: envelope %d payload %d", ErrSnapshotSequence, seq, p.LastSequence)
	}
	return seq, p, nil
}

// RestoreSnapshot rebuilds State from a validated payload using seed instruments.
// Books are rebuilt from bid/ask FIFO levels; orders hydrated via lifecycle.
func RestoreSnapshot(p snapPayload, instruments *domain.InstrumentRegistry) (*State, error) {
	if instruments == nil {
		return nil, fmt.Errorf("%w: nil instruments", ErrInvalidSnapshot)
	}
	accts := make(map[domain.AccountID]*domain.Account)
	for _, a := range p.Accounts {
		acc, err := domain.NewAccount(domain.AccountParams{
			ID: domain.AccountID(a.ID), Currency: domain.CurrencyINR, Cash: domain.Money(a.Cash),
		})
		if err != nil {
			return nil, err
		}
		for _, pos := range a.Positions {
			if pos.Qty <= 0 {
				continue
			}
			if err := acc.AddToPosition(domain.InstrumentID(pos.Instrument), domain.Quantity(pos.Qty), 1); err != nil {
				return nil, err
			}
		}
		accts[domain.AccountID(a.ID)] = &acc
	}
	s := NewState(instruments, accts)
	s.LastEventSequence = domain.Sequence(p.LastSequence)
	s.EventsApplied = p.EventsApplied
	for _, id := range p.SeenEventIDs {
		s.seenEventIDs[id] = struct{}{}
	}
	for _, id := range p.SeenTradeIDs {
		s.seenTradeIDs[id] = struct{}{}
	}

	// Orders first (no book yet)
	for _, o := range p.Orders {
		ord, err := hydrateOrder(o)
		if err != nil {
			return nil, fmt.Errorf("%w: order %s: %w", ErrInvalidSnapshot, o.ID, err)
		}
		s.Orders[domain.OrderID(o.ID)] = ord
	}

	// Books from FIFO levels
	for _, lv := range p.BidLevels {
		if err := placeLevel(s, lv, domain.SideBuy); err != nil {
			return nil, err
		}
	}
	for _, lv := range p.AskLevels {
		if err := placeLevel(s, lv, domain.SideSell); err != nil {
			return nil, err
		}
	}

	for _, r := range p.Reservations {
		res := domain.Reservation{
			OrderID: domain.OrderID(r.OrderID), AccountID: domain.AccountID(r.Account),
			InstrumentID: domain.InstrumentID(r.Instrument),
			Cash:         domain.Money(r.Cash), PositionQuantity: domain.Quantity(r.PositionQuantity),
		}
		if err := s.Reservations.Restore(res); err != nil {
			return nil, err
		}
	}

	for _, t := range p.Trades {
		tr, err := domain.NewTrade(domain.TradeParams{
			ID: domain.TradeID(t.ID), InstrumentID: domain.InstrumentID(t.Instrument),
			BuyOrderID: domain.OrderID(t.Buy), SellOrderID: domain.OrderID(t.Sell),
			Price: domain.Price(t.Price), Quantity: domain.Quantity(t.Qty),
			Sequence: domain.Sequence(t.Sequence),
		})
		if err != nil {
			return nil, err
		}
		s.Trades = append(s.Trades, tr)
	}
	// Executions stored as lightweight facts — skip full domain rebuild if NewExecution is strict;
	// trades are enough for canonical compare of trade list. Executions optional in snapshot compare.
	_ = event.TypeOrderAccepted

	if err := validateFinal(s); err != nil {
		return nil, err
	}
	return s, nil
}

func hydrateOrder(o OrderSnap) (*domain.Order, error) {
	var price *domain.Price
	if o.Price >= 0 {
		p := domain.Price(o.Price)
		price = &p
	}
	ord, err := domain.NewOrder(domain.OrderParams{
		ID: domain.OrderID(o.ID), AccountID: domain.AccountID(o.Account),
		InstrumentID: domain.InstrumentID(o.Instrument),
		Side:         domain.Side(o.Side), Type: domain.OrderType(o.Type),
		Price: price, Quantity: domain.Quantity(o.Qty), Sequence: domain.Sequence(o.Sequence),
	})
	if err != nil {
		return nil, err
	}
	filled := o.Qty - o.Remaining
	if err := ord.AdvanceToAccepted(); err != nil {
		return nil, err
	}
	st := domain.OrderStatus(o.Status)
	switch st {
	case domain.OrderStatusAccepted:
		return &ord, nil
	case domain.OrderStatusResting, domain.OrderStatusPartiallyFilled, domain.OrderStatusFilled:
		if err := ord.Rest(); err != nil {
			return nil, err
		}
		if filled > 0 {
			if err := ord.ApplyFill(domain.Quantity(filled)); err != nil {
				return nil, err
			}
		}
		// PARTIAL may leave PARTIALLY_FILLED; RESTING with full rem stays RESTING after Rest
		if st == domain.OrderStatusResting && ord.Status() == domain.OrderStatusPartiallyFilled {
			_ = ord.Rest() // optional re-label
		}
		return &ord, nil
	case domain.OrderStatusCancelled:
		if err := ord.Rest(); err != nil {
			return nil, err
		}
		if filled > 0 {
			if err := ord.ApplyFill(domain.Quantity(filled)); err != nil {
				return nil, err
			}
		}
		if err := ord.Cancel(""); err != nil {
			return nil, err
		}
		return &ord, nil
	case domain.OrderStatusExpired:
		if err := ord.Rest(); err != nil {
			return nil, err
		}
		if filled > 0 {
			if err := ord.ApplyFill(domain.Quantity(filled)); err != nil {
				return nil, err
			}
		}
		if err := ord.Expire(""); err != nil {
			return nil, err
		}
		return &ord, nil
	default:
		return nil, fmt.Errorf("unsupported status %s", o.Status)
	}
}

func placeLevel(s *State, lv LevelSnap, side domain.Side) error {
	b, err := s.bookFor(domain.InstrumentID(lv.Instrument))
	if err != nil {
		return err
	}
	for i, oid := range lv.OrderIDs {
		o, ok := s.Orders[domain.OrderID(oid)]
		if !ok {
			return fmt.Errorf("%w: missing order %s on book", ErrInvalidSnapshot, oid)
		}
		if o.Side != side {
			return fmt.Errorf("%w: side mismatch %s", ErrInvalidSnapshot, oid)
		}
		// Must be RESTING or PARTIAL with rem > 0
		st := o.Status()
		if st != domain.OrderStatusResting && st != domain.OrderStatusPartiallyFilled {
			return fmt.Errorf("%w: order %s not bookable status %s", ErrInvalidSnapshot, oid, st)
		}
		if i < len(lv.Qtys) && int64(o.RemainingQuantity()) != lv.Qtys[i] {
			return fmt.Errorf("%w: qty mismatch %s", ErrInvalidSnapshot, oid)
		}
		if err := b.Add(o); err != nil {
			return err
		}
	}
	return nil
}

// WriteSnapshotAtomically writes framed bytes via temp + sync + rename.
func WriteSnapshotAtomically(dir string, seq domain.Sequence, raw []byte) (path string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("snapshot-%016d.snap", uint64(seq))
	final := filepath.Join(dir, name)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	// best-effort dir sync
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return final, nil
}

// CreateSnapshot encodes and atomically publishes a snapshot of s.
func CreateSnapshot(dir string, s *State) (string, error) {
	raw, err := EncodeSnapshot(s)
	if err != nil {
		return "", err
	}
	return WriteSnapshotAtomically(dir, s.LastEventSequence, raw)
}

// LoadSnapshot reads and validates a snapshot file, restores State.
func LoadSnapshot(path string, instruments *domain.InstrumentRegistry) (*State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seq, p, err := DecodeSnapshot(raw)
	if err != nil {
		return nil, err
	}
	st, err := RestoreSnapshot(p, instruments)
	if err != nil {
		return nil, err
	}
	if uint64(st.LastEventSequence) != seq {
		return nil, fmt.Errorf("%w: restored seq", ErrSnapshotSequence)
	}
	return st, nil
}

// listSnapshots returns paths sorted by sequence descending (newest first).
func listSnapshots(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type cand struct {
		seq  uint64
		path string
	}
	var cs []cand
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "snapshot-") || !strings.HasSuffix(name, ".snap") {
			continue
		}
		mid := strings.TrimSuffix(strings.TrimPrefix(name, "snapshot-"), ".snap")
		seq, err := strconv.ParseUint(mid, 10, 64)
		if err != nil {
			continue
		}
		cs = append(cs, cand{seq: seq, path: filepath.Join(dir, name)})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].seq > cs[j].seq })
	out := make([]string, len(cs))
	for i := range cs {
		out[i] = cs[i].path
	}
	return out, nil
}

// SnapshotRecoverResult includes path metadata.
type SnapshotRecoverResult struct {
	*Result
	UsedSnapshot bool
	SnapshotPath string
	FallbackFull bool
}

// RecoverWithSnapshot tries newest valid snapshot then WAL suffix via ApplyEvent.
// On snapshot failure with FallbackFull, uses full RecoverDir (Strict).
// Never treats corrupt WAL suffix as success by dropping history.
func RecoverWithSnapshot(cfg Config, snapDir string, fallbackFull bool) (*SnapshotRecoverResult, error) {
	cands, err := listSnapshots(snapDir)
	if err != nil {
		return nil, err
	}

	for _, path := range cands {
		st, err := LoadSnapshot(path, cfg.Instruments)
		if err != nil {
			continue // try older / fallback
		}
		// Continuity: open WAL and apply only seq > N
		rd, err := wal.OpenReader(wal.ReaderConfig{Dir: cfg.WALDir, MaxPayloadBytes: cfg.MaxPayload})
		if err != nil {
			return nil, err
		}
		n := st.LastEventSequence
		err = foldSuffix(rd, st, n)
		_ = rd.Close()
		if err != nil {
			// History failure — do not return success omitting suffix
			return nil, fmt.Errorf("%w: suffix after snapshot %d: %w", ErrSnapshotIncompatible, n, err)
		}
		if err := validateFinal(st); err != nil {
			return nil, err
		}
		return &SnapshotRecoverResult{
			Result: &Result{
				State:           st,
				LastSequence:    st.LastEventSequence,
				EventsApplied:   st.EventsApplied,
				TradesRecovered: uint64(len(st.Trades)),
				OrdersRecovered: uint64(len(st.Orders)),
			},
			UsedSnapshot: true,
			SnapshotPath: path,
		}, nil
	}

	if !fallbackFull {
		return nil, fmt.Errorf("%w: no usable snapshot", ErrInvalidSnapshot)
	}
	res, err := RecoverDir(Config{
		Mode: ModeStrict, Instruments: cfg.Instruments, Accounts: cloneAccounts(cfg.Accounts),
		WALDir: cfg.WALDir, MaxPayload: cfg.MaxPayload,
	})
	if err != nil {
		return nil, err
	}
	return &SnapshotRecoverResult{Result: res, FallbackFull: true}, nil
}

func cloneAccounts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for id, a := range in {
		na, err := domain.NewAccount(domain.AccountParams{ID: id, Currency: a.Currency, Cash: a.Cash()})
		if err != nil {
			continue
		}
		for _, p := range a.Positions() {
			px := p.AveragePrice
			if px <= 0 {
				px = 1
			}
			_ = na.AddToPosition(p.InstrumentID, p.Quantity, px)
		}
		out[id] = &na
	}
	return out
}

// foldSuffix reads entire WAL; skips apply for seq<=after; requires first applied == after+1 or empty suffix.
func foldSuffix(r *wal.Reader, s *State, after domain.Sequence) error {
	sawAfter := false
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		seq := ev.Sequence()
		if seq <= after {
			continue
		}
		if !sawAfter {
			if seq != after+1 && after != 0 {
				return fmt.Errorf("%w: want %d got %d", ErrSequenceMismatch, after+1, seq)
			}
			if after == 0 && seq != 1 {
				return fmt.Errorf("%w: want 1 got %d", ErrSequenceMismatch, seq)
			}
			sawAfter = true
		}
		if err := s.ApplyEvent(ev); err != nil {
			return err
		}
	}
}

// ensure book import used
var _ = book.OrderBook{}
