package domain

import "fmt"

// AffordabilityResult is a pure query result: no state is mutated.
type AffordabilityResult struct {
	Side              Side
	RequiredCash      Money
	AvailableCash     Money
	RequiredPosition  Quantity
	AvailablePosition Quantity
	Affordable        bool
}

// BuyingPower is current cash capacity before reservations (P4.3).
// With ReservedCash = 0 by definition at this milestone:
//
//	BuyingPower = Cash
func (a Account) BuyingPower() Money {
	return a.Cash()
}

// CanAfford evaluates whether account can support order under M4.3 rules.
//
// BUY: required cash = Quantity × price (limit price or reference for MARKET).
// SELL: required position = Quantity vs owned holdings (no shorts).
//
// Mutation: NONE. Does not debit cash, change positions, or touch the book.
//
// refPrice is required for MARKET BUY; ignored for LIMIT (uses order price)
// and for SELL (inventory only).
func CanAfford(a Account, inst Instrument, order Order, refPrice *Price) (AffordabilityResult, error) {
	res := AffordabilityResult{
		Side:          order.Side,
		AvailableCash: a.Cash(),
	}
	pos, _ := a.GetPosition(order.InstrumentID)
	res.AvailablePosition = pos.Quantity

	if err := a.Validate(); err != nil {
		return res, fmt.Errorf("%w: account: %w", ErrBuyingPower, err)
	}
	if err := inst.Validate(); err != nil {
		return res, fmt.Errorf("%w: instrument: %w", ErrBuyingPower, err)
	}
	if order.InstrumentID != inst.ID {
		return res, fmt.Errorf("%w: order instrument %s != %s", ErrBuyingPower, order.InstrumentID, inst.ID)
	}
	if a.Currency != inst.Currency {
		return res, fmt.Errorf("%w: account %s vs instrument %s", ErrCurrencyMismatch, a.Currency, inst.Currency)
	}
	if order.Quantity <= 0 {
		return res, fmt.Errorf("%w: %w", ErrBuyingPower, ErrInvalidQuantity)
	}

	switch order.Side {
	case SideBuy:
		return affordBuy(a, inst, order, refPrice, res)
	case SideSell:
		return affordSell(order, res)
	default:
		return res, fmt.Errorf("%w: side %s", ErrBuyingPower, order.Side)
	}
}

func affordBuy(a Account, inst Instrument, order Order, refPrice *Price, res AffordabilityResult) (AffordabilityResult, error) {
	var px Price
	switch order.Type {
	case OrderTypeLimit:
		p, ok := order.Price()
		if !ok {
			return res, fmt.Errorf("%w: limit buy missing price", ErrBuyingPower)
		}
		px = p
	case OrderTypeMarket:
		if refPrice == nil {
			return res, fmt.Errorf("%w: MARKET BUY", ErrReferencePriceRequired)
		}
		if *refPrice <= 0 {
			return res, fmt.Errorf("%w: reference price must be > 0", ErrBuyingPower)
		}
		px = *refPrice
	default:
		return res, fmt.Errorf("%w: order type %s", ErrBuyingPower, order.Type)
	}

	unit, err := inst.MoneyFromPrice(px)
	if err != nil {
		return res, fmt.Errorf("%w: price to money: %w", ErrBuyingPower, err)
	}
	notional, err := CheckedMulMoneyQty(unit, order.Quantity)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrNotionalOverflow, err)
	}
	res.RequiredCash = notional
	if notional > a.Cash() {
		return res, fmt.Errorf("%w: need %d have %d", ErrInsufficientBuyingPower, notional, a.Cash())
	}
	res.Affordable = true
	return res, nil
}

func affordSell(order Order, res AffordabilityResult) (AffordabilityResult, error) {
	res.RequiredPosition = order.Quantity
	if order.Quantity > res.AvailablePosition {
		return res, fmt.Errorf("%w: need %d have %d", ErrInsufficientPosition, order.Quantity, res.AvailablePosition)
	}
	res.Affordable = true
	return res, nil
}
