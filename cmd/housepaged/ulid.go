package main

import (
	"crypto/rand"
	"sync"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // no I, L, O or U

// ulidGen makes ULIDs: a 48-bit millisecond timestamp and 80 random bits, as 26 Crockford base32 characters.
// Ids made in the same millisecond still ascend, because the random part is incremented instead of redrawn.
type ulidGen struct {
	mu     sync.Mutex
	now    func() int64 // unix milliseconds; a test swaps it
	lastMs int64
	rnd    [10]byte
}

func newULIDGen() *ulidGen {
	return &ulidGen{now: func() int64 { return time.Now().UnixMilli() }}
}

func (g *ulidGen) next() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := g.now()
	if ms <= g.lastMs {
		// Same millisecond (or the clock stepped back): keep the old time so ids never go backwards.
		ms = g.lastMs
		i := len(g.rnd) - 1
		for ; i >= 0; i-- {
			g.rnd[i]++
			if g.rnd[i] != 0 {
				break
			}
		}
		if i < 0 { // 2^80 ids in one millisecond cannot happen, but a wrapped counter would repeat ids
			g.lastMs++
			ms = g.lastMs
			if _, err := rand.Read(g.rnd[:]); err != nil {
				return "", err
			}
		}
	} else {
		g.lastMs = ms
		if _, err := rand.Read(g.rnd[:]); err != nil {
			return "", err
		}
	}
	var b [26]byte
	for i := 9; i >= 0; i-- { // 10 characters, 5 bits each, of the 48-bit time
		b[i] = crockford[ms&31]
		ms >>= 5
	}
	// 80 random bits as 16 characters
	var acc uint16
	bits, j := 0, 10
	for _, by := range g.rnd {
		acc = acc<<8 | uint16(by)
		bits += 8
		for bits >= 5 {
			bits -= 5
			b[j] = crockford[(acc>>uint(bits))&31]
			j++
		}
	}
	return string(b[:]), nil
}

var ids = newULIDGen()

// newID is a package variable so a test can force a repeated id.
var newID = ids.next
