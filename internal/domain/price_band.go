package domain

import (
	"fmt"
)

// PriceBand is a static inclusive trading-price interval for one instrument.
//
//	MinPrice <= limitOrderPrice <= MaxPrice
//
// Distinct from Instrument.MinPrice/MaxPrice (structural validity / tick model).
// P4.6 is trading admissibility policy — not volatility bands, circuit breakers,
// or book-derived prices.
//
// MARKET orders have no submitted price; static band check is not applicable.
type PriceBand struct {
	InstrumentID InstrumentID
	MinPrice     Price
	MaxPrice     Price
}

// Validate checks band structural invariants (not tick alignment).
func (b PriceBand) Validate() error {
	if b.InstrumentID == "" {
		return fmt.Errorf("%w: missing instrument id", ErrInvalidPriceBand)
	}
	if b.MinPrice < 0 {
		return fmt.Errorf("%w: min price must be >= 0", ErrInvalidPriceBand)
	}
	if b.MaxPrice < b.MinPrice {
		return fmt.Errorf("%w: min %d > max %d", ErrInvalidPriceBand, b.MinPrice, b.MaxPrice)
	}
	return nil
}

// Contains reports whether p lies in [MinPrice, MaxPrice] inclusive.
func (b PriceBand) Contains(p Price) bool {
	return p >= b.MinPrice && p <= b.MaxPrice
}

// PriceBandStore holds optional per-instrument static price bands.
//
// Missing key → no price-band restriction.
// No book/matching/market-data dependency. No mutex in P4.6.
type PriceBandStore struct {
	byInstrument map[InstrumentID]PriceBand
}

// NewPriceBandStore creates an empty store (all instruments unrestricted).
func NewPriceBandStore() *PriceBandStore {
	return &PriceBandStore{byInstrument: make(map[InstrumentID]PriceBand)}
}

// Set configures or updates the band for an instrument.
func (s *PriceBandStore) Set(instrumentID InstrumentID, minPrice, maxPrice Price) error {
	b := PriceBand{InstrumentID: instrumentID, MinPrice: minPrice, MaxPrice: maxPrice}
	if err := b.Validate(); err != nil {
		return err
	}
	if s.byInstrument == nil {
		s.byInstrument = make(map[InstrumentID]PriceBand)
	}
	s.byInstrument[instrumentID] = b
	return nil
}

// Get returns the configured band if present.
func (s *PriceBandStore) Get(instrumentID InstrumentID) (PriceBand, bool) {
	if s == nil || s.byInstrument == nil {
		return PriceBand{}, false
	}
	b, ok := s.byInstrument[instrumentID]
	return b, ok
}

// Delete clears the band (instrument becomes unrestricted by P4.6).
func (s *PriceBandStore) Delete(instrumentID InstrumentID) {
	if s == nil || s.byInstrument == nil {
		return
	}
	delete(s.byInstrument, instrumentID)
}

// PriceBandResult is a pure evaluation of an order against the static band.
type PriceBandResult struct {
	HasBand    bool
	Applicable bool // false for MARKET / no price
	OrderPrice Price
	MinPrice   Price
	MaxPrice   Price
	Allowed    bool
}

// CheckPriceBand evaluates whether a LIMIT order's price is inside the band.
//
// Pure query: no mutation of order, instrument, store, account, or book.
// No BestBid/Ask, last trade, VWAP, or reference price.
//
//	missing band          → PASS (unrestricted)
//	MARKET / nil price    → PASS (not applicable)
//	LIMIT with price      → PASS iff Min <= price <= Max (inclusive)
//
// Tick alignment remains Instrument.ValidatePrice — not duplicated here.
func CheckPriceBand(
	store *PriceBandStore,
	instrumentID InstrumentID,
	orderType OrderType,
	limitPrice *Price,
) (PriceBandResult, error) {
	res := PriceBandResult{}

	band, ok := priceBandGet(store, instrumentID)
	if !ok {
		res.Allowed = true
		return res, nil
	}
	res.HasBand = true
	res.MinPrice = band.MinPrice
	res.MaxPrice = band.MaxPrice

	// MARKET (and any order without a submitted price): static band N/A.
	if orderType == OrderTypeMarket || limitPrice == nil {
		res.Applicable = false
		res.Allowed = true
		return res, nil
	}

	res.Applicable = true
	res.OrderPrice = *limitPrice

	if !band.Contains(*limitPrice) {
		return res, fmt.Errorf(
			"%w: instrument=%s price=%d band=[%d,%d]",
			ErrPriceBandViolation,
			instrumentID, *limitPrice, band.MinPrice, band.MaxPrice,
		)
	}
	res.Allowed = true
	return res, nil
}

// CheckPriceBandForOrder convenience using Order fields.
func CheckPriceBandForOrder(store *PriceBandStore, o Order) (PriceBandResult, error) {
	var limitPrice *Price
	if p, ok := o.Price(); ok {
		pp := p
		limitPrice = &pp
	}
	return CheckPriceBand(store, o.InstrumentID, o.Type, limitPrice)
}

func priceBandGet(store *PriceBandStore, instrumentID InstrumentID) (PriceBand, bool) {
	if store == nil {
		return PriceBand{}, false
	}
	return store.Get(instrumentID)
}
