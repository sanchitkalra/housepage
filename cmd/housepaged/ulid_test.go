package main

import (
	"sort"
	"strings"
	"testing"
)

func fakeClock(ms *int64) *ulidGen { return &ulidGen{now: func() int64 { return *ms }} }

func mustNext(t *testing.T, g *ulidGen) string {
	t.Helper()
	id, err := g.next()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestULIDShapeAndAlphabet(t *testing.T) {
	g := newULIDGen()
	for i := 0; i < 1000; i++ {
		id := mustNext(t, g)
		if len(id) != 26 {
			t.Fatalf("%q has %d characters, want 26", id, len(id))
		}
		for _, c := range id {
			if !strings.ContainsRune(crockford, c) {
				t.Fatalf("%q contains %q, not in the Crockford alphabet", id, c)
			}
		}
	}
}

func TestULIDKnownTimestampEncoding(t *testing.T) {
	ms := int64(1760900000000)
	id := mustNext(t, fakeClock(&ms))
	// the first 10 characters are the 48-bit time in base32: decode and compare
	var got int64
	for _, c := range id[:10] {
		got = got<<5 | int64(strings.IndexRune(crockford, c))
	}
	if got != ms {
		t.Errorf("timestamp part of %q decodes to %d, want %d", id, got, ms)
	}
}

func TestULIDsSortInGenerationOrder(t *testing.T) {
	ms := int64(1000)
	g := fakeClock(&ms)
	var all []string
	for i := 0; i < 200; i++ {
		if i%7 == 0 {
			ms += 1 + int64(i%3) // sometimes the clock moves on
		}
		all = append(all, mustNext(t, g))
	}
	if !sort.StringsAreSorted(all) {
		t.Error("ids do not sort in the order they were made")
	}
}

func TestSameMillisecondIDsStillAscend(t *testing.T) {
	ms := int64(5000)
	g := fakeClock(&ms)
	prev := mustNext(t, g)
	for i := 0; i < 5000; i++ {
		id := mustNext(t, g)
		if id <= prev {
			t.Fatalf("%q came after %q in the same millisecond", id, prev)
		}
		prev = id
	}
}

func TestClockGoingBackwardsDoesNotReorder(t *testing.T) {
	ms := int64(5000)
	g := fakeClock(&ms)
	a := mustNext(t, g)
	ms = 4000
	if b := mustNext(t, g); b <= a {
		t.Errorf("%q after %q when the clock stepped back", b, a)
	}
}

func TestCarryAcrossTheRandomBytes(t *testing.T) {
	ms := int64(5000)
	g := fakeClock(&ms)
	mustNext(t, g)
	for i := range g.rnd {
		g.rnd[i] = 0xff
	}
	g.rnd[0] = 0x00 // the carry stops at the first byte
	a := mustNext(t, g)
	b := mustNext(t, g)
	if b <= a {
		t.Errorf("%q after %q across a byte carry", b, a)
	}
}

func TestULIDsAreUnique(t *testing.T) {
	g := newULIDGen()
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id := mustNext(t, g)
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
