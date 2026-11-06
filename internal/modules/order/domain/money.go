//go:build examples

package domain

import "strings"

// Money is an immutable amount in minor units (e.g. cents) with an ISO 4217 code.
type Money struct {
	minor    int64
	currency string
}

// NewMoney validates and normalises a positive amount.
func NewMoney(minor int64, currency string) (Money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if minor <= 0 || len(currency) != 3 {
		return Money{}, ErrInvalidOrder
	}
	for _, r := range currency {
		if r < 'A' || r > 'Z' {
			return Money{}, ErrInvalidOrder
		}
	}
	return Money{minor: minor, currency: currency}, nil
}

func (m Money) Minor() int64     { return m.minor }
func (m Money) Currency() string { return m.currency }
func (m Money) IsPositive() bool { return m.minor > 0 }
