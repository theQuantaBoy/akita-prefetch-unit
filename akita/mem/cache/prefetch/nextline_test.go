package prefetch

import (
	"reflect"
	"testing"
)

// nextLineBaseConfig returns a NextLineConfig with 64-byte blocks, 4KiB
// pages, and a lookahead of 3, which most tests below tune from.
func nextLineBaseConfig() NextLineConfig {
	return NextLineConfig{
		Degree:        3,
		Log2BlockSize: 6,  // 64-byte blocks
		Log2PageSize:  12, // 4KiB pages
	}
}

func TestNextLinePrefetcher_SequentialCandidates(t *testing.T) {
	p := NewNextLinePrefetcher(nextLineBaseConfig())

	got := p.Access(1, 0x1000)
	want := []uint64{0x1040, 0x1080, 0x10C0}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got candidates %v, want %v", got, want)
	}
}

func TestNextLinePrefetcher_BlockAlignment(t *testing.T) {
	cfg := nextLineBaseConfig()
	cfg.Degree = 1

	p := NewNextLinePrefetcher(cfg)

	// 0x1010 and 0x103F both fall inside the 64-byte block starting at
	// 0x1000, so both must predict the same next block, 0x1040.
	for _, addr := range []uint64{0x1000, 0x1010, 0x103F} {
		got := p.Access(1, addr)
		want := []uint64{0x1040}

		if !reflect.DeepEqual(got, want) {
			t.Errorf("Access(_, 0x%x) = %v, want %v", addr, got, want)
		}
	}
}

func TestNextLinePrefetcher_PageBoundaryTruncation(t *testing.T) {
	p := NewNextLinePrefetcher(nextLineBaseConfig()) // Degree 3, 4KiB pages

	// 0x1F80 is 3 blocks (0xC0 bytes) from the 0x2000 page boundary, so
	// only the first 2 of 3 candidates (0x1FC0, 0x2000 excluded) stay on
	// the same page as the trigger.
	got := p.Access(1, 0x1F80)
	want := []uint64{0x1FC0}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got candidates %v, want %v", got, want)
	}
}

func TestNextLinePrefetcher_PageBoundaryTruncation_ImmediateCrossing(t *testing.T) {
	p := NewNextLinePrefetcher(nextLineBaseConfig())

	// The very last block of a page: even the first candidate crosses.
	got := p.Access(1, 0x1FC0)

	if got != nil {
		t.Fatalf("got candidates %v, want nil", got)
	}
}

// TestNextLinePrefetcher_Stateless verifies that, unlike StridePrefetcher,
// repeated or differently-streamed accesses to the same address always
// produce the same prediction: there is no learned state to build up or
// break.
func TestNextLinePrefetcher_Stateless(t *testing.T) {
	p := NewNextLinePrefetcher(nextLineBaseConfig())

	first := p.Access(1, 0x1000)
	second := p.Access(1, 0x1000)
	thirdOnDifferentStream := p.Access(2, 0x1000)

	want := []uint64{0x1040, 0x1080, 0x10C0}

	for name, got := range map[string][]uint64{
		"first access":                first,
		"repeated access":             second,
		"different stream, same addr": thirdOnDifferentStream,
	} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}

// TestNextLinePrefetcher_Reset verifies Reset is a harmless no-op: since
// NextLinePrefetcher has no state, behavior is identical before and after.
func TestNextLinePrefetcher_Reset(t *testing.T) {
	p := NewNextLinePrefetcher(nextLineBaseConfig())

	before := p.Access(1, 0x1000)
	p.Reset()
	after := p.Access(1, 0x1000)

	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Access before Reset = %v, after Reset = %v; want identical", before, after)
	}
}

func TestNewNextLinePrefetcher_PanicsOnInvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(cfg *NextLineConfig)
		wantPanic bool
	}{
		{
			name:      "valid config does not panic",
			mutate:    func(cfg *NextLineConfig) {},
			wantPanic: false,
		},
		{
			name:      "zero Degree panics",
			mutate:    func(cfg *NextLineConfig) { cfg.Degree = 0 },
			wantPanic: true,
		},
		{
			name:      "negative Degree panics",
			mutate:    func(cfg *NextLineConfig) { cfg.Degree = -1 },
			wantPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := nextLineBaseConfig()
			tt.mutate(&cfg)

			defer func() {
				r := recover()
				gotPanic := r != nil

				if gotPanic != tt.wantPanic {
					t.Errorf("panic = %v (recovered %v), want panic = %v", gotPanic, r, tt.wantPanic)
				}
			}()

			NewNextLinePrefetcher(cfg)
		})
	}
}

// TestNextLinePrefetcher_ImplementsPrefetcher is a compile-time-flavored
// sanity check that NextLinePrefetcher can be used through the Prefetcher
// interface, as it is intended to be plugged in behind.
func TestNextLinePrefetcher_ImplementsPrefetcher(t *testing.T) {
	var p Prefetcher = NewNextLinePrefetcher(nextLineBaseConfig())

	if got := p.Access(1, 0x1000); len(got) != 3 {
		t.Fatalf("first access through interface: got %v, want 3 candidates", got)
	}

	p.Reset()
}
