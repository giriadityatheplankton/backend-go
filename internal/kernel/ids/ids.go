// Package ids generates opaque unique identifiers. Standard library only.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Random generates 128-bit random hex identifiers.
type Random struct{}

// NewID returns a 32 character hex string. It panics only if the OS entropy
// source is broken, which is unrecoverable.
func (Random) NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("ids: entropy source failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// Sequence returns deterministic IDs ("id-1", "id-2", ...); for tests.
type Sequence struct{ n int }

func (s *Sequence) NewID() string { s.n++; return fmt.Sprintf("id-%d", s.n) }
