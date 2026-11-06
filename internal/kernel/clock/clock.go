// Package clock abstracts time so use cases can be tested deterministically.
package clock

import "time"

// Clock returns the current time.
type Clock interface {
	Now() time.Time
}

// System is the wall clock in UTC.
type System struct{}

func (System) Now() time.Time { return time.Now().UTC() }

// Fixed always returns the same instant; for tests.
type Fixed struct{ T time.Time }

func (f Fixed) Now() time.Time { return f.T }
