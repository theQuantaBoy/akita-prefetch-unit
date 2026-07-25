package prefetch

import (
	"reflect"
	"testing"
)

// baseConfig returns a StrideConfig with 64-byte blocks and 4KiB pages,
// which every test below tunes ConfidenceThreshold/Degree/MaxStreams from.
func baseConfig() StrideConfig {
	return StrideConfig{
		ConfidenceThreshold: 2,
		Degree:              3,
		Log2BlockSize:       6,  // 64-byte blocks
		Log2PageSize:        12, // 4KiB pages
		MaxStreams:          4,
	}
}

func assertNilCandidates(t *testing.T, got []uint64, ctx string) {
	t.Helper()

	if len(got) != 0 {
		t.Fatalf("%s: got candidates %v, want none", ctx, got)
	}
}

func assertCandidates(t *testing.T, got []uint64, want []uint64, ctx string) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got candidates %v, want %v", ctx, got, want)
	}
}

func assertLookup(
	t *testing.T,
	p *StridePrefetcher,
	stream StreamID,
	wantState State,
	wantConfidence uint32,
	wantOK bool,
	ctx string,
) {
	t.Helper()

	state, confidence, ok := p.Lookup(stream)
	if ok != wantOK {
		t.Fatalf("%s: Lookup ok = %v, want %v", ctx, ok, wantOK)
	}

	if !wantOK {
		return
	}

	if state != wantState {
		t.Errorf("%s: state = %v, want %v", ctx, state, wantState)
	}

	if confidence != wantConfidence {
		t.Errorf("%s: confidence = %d, want %d", ctx, confidence, wantConfidence)
	}
}

// TestStridePrefetcher_LearnsStride walks a stream through the full
// Initial -> Transient -> Steady progression on a fixed-offset access
// pattern, checking both the confidence state after each access and the
// candidates it produces (none until Steady, then Degree of them).
func TestStridePrefetcher_LearnsStride(t *testing.T) {
	p := NewStridePrefetcher(baseConfig())
	const stream StreamID = 1

	assertNilCandidates(t, p.Access(stream, 0x1000), "1st access")
	assertLookup(t, p, stream, StateInitial, 0, true, "after 1st access")

	assertNilCandidates(t, p.Access(stream, 0x1040), "2nd access")
	assertLookup(t, p, stream, StateTransient, 1, true, "after 2nd access")

	got := p.Access(stream, 0x1080)
	assertCandidates(t, got, []uint64{0x10C0, 0x1100, 0x1140}, "3rd access")
	assertLookup(t, p, stream, StateSteady, 2, true, "after 3rd access")

	// Confidence saturates at ConfidenceThreshold and keeps predicting.
	got = p.Access(stream, 0x10C0)
	assertCandidates(t, got, []uint64{0x1100, 0x1140, 0x1180}, "4th access")
	assertLookup(t, p, stream, StateSteady, 2, true, "after 4th access")
}

// TestStridePrefetcher_MismatchUnlearns verifies that an access breaking
// the learned stride resets confidence and immediately stops predicting,
// but the newly observed delta becomes the fresh guess the stream then
// relearns from.
func TestStridePrefetcher_MismatchUnlearns(t *testing.T) {
	cfg := baseConfig()
	cfg.ConfidenceThreshold = 3
	cfg.Degree = 2

	p := NewStridePrefetcher(cfg)
	const stream StreamID = 1

	p.Access(stream, 0x000)
	p.Access(stream, 0x040) // stride 0x40, confidence 1
	p.Access(stream, 0x080) // confidence 2

	got := p.Access(stream, 0x0C0) // confidence 3 -> Steady
	assertCandidates(t, got, []uint64{0x100, 0x140}, "reaches steady")
	assertLookup(t, p, stream, StateSteady, 3, true, "steady on old stride")

	// Delta from 0xC0 to 0x140 is 0x80, which does not match the learned
	// stride of 0x40: this must unlearn the stream back down.
	got = p.Access(stream, 0x140)
	assertNilCandidates(t, got, "mismatch access")
	assertLookup(t, p, stream, StateTransient, 1, true, "right after mismatch")

	// The stream should now be learning the new 0x80 stride from scratch.
	p.Access(stream, 0x1C0) // matches 0x80, confidence 2
	assertLookup(t, p, stream, StateTransient, 2, true, "relearning new stride")

	got = p.Access(stream, 0x240) // matches 0x80 again, confidence 3 -> Steady
	assertCandidates(t, got, []uint64{0x2C0, 0x340}, "relearned stride steady")
}

// TestStridePrefetcher_RepeatedAccessIsNoOp verifies that re-accessing the
// same block leaves the stream's learned state untouched.
func TestStridePrefetcher_RepeatedAccessIsNoOp(t *testing.T) {
	p := NewStridePrefetcher(baseConfig())
	const stream StreamID = 1

	p.Access(stream, 0x1000)
	assertLookup(t, p, stream, StateInitial, 0, true, "after 1st access")

	got := p.Access(stream, 0x1000)
	assertNilCandidates(t, got, "repeated access")
	assertLookup(t, p, stream, StateInitial, 0, true, "after repeated access")
}

// TestStridePrefetcher_BlockAlignment verifies that accesses are aligned to
// Log2BlockSize before stride detection, so two addresses within the same
// block never register as a stride change.
func TestStridePrefetcher_BlockAlignment(t *testing.T) {
	p := NewStridePrefetcher(baseConfig()) // 64-byte blocks
	const stream StreamID = 1

	p.Access(stream, 0x1000)
	// Same block as 0x1000 (block size 0x40), despite a different byte
	// offset: must not be treated as a new stride.
	got := p.Access(stream, 0x1010)
	assertNilCandidates(t, got, "same-block access")
	assertLookup(t, p, stream, StateInitial, 0, true, "still initial")

	// Next block over: establishes a one-block stride.
	p.Access(stream, 0x1044) // aligns to 0x1040
	assertLookup(t, p, stream, StateTransient, 1, true, "stride established")

	got = p.Access(stream, 0x1090) // aligns to 0x1080
	assertCandidates(t, got, []uint64{0x10C0, 0x1100, 0x1140}, "steady after alignment")
}

// TestStridePrefetcher_MultiStepLookahead verifies that once a stream is
// Steady, Access returns exactly Degree candidates, nearest-first.
func TestStridePrefetcher_MultiStepLookahead(t *testing.T) {
	cfg := baseConfig()
	cfg.ConfidenceThreshold = 1
	cfg.Degree = 4
	cfg.Log2BlockSize = 4 // 16-byte blocks
	cfg.Log2PageSize = 20 // 1MiB pages, far from any boundary here

	p := NewStridePrefetcher(cfg)
	const stream StreamID = 1

	assertNilCandidates(t, p.Access(stream, 0x0000), "1st access")

	got := p.Access(stream, 0x0010) // confidence 1 already meets threshold 1
	assertCandidates(t, got, []uint64{0x20, 0x30, 0x40, 0x50}, "lookahead")
}

// TestStridePrefetcher_PageBoundarySuppression verifies that candidates
// crossing onto a different page than the triggering access are dropped,
// in both the positive- and negative-stride directions.
func TestStridePrefetcher_PageBoundarySuppression(t *testing.T) {
	p := NewStridePrefetcher(baseConfig())

	t.Run("forward stride truncates at the boundary", func(t *testing.T) {
		const stream StreamID = 1

		p.Access(stream, 0x1F00)
		p.Access(stream, 0x1F40) // stride +0x40

		got := p.Access(stream, 0x1F80) // Steady; 0x2000 candidate crosses
		assertCandidates(t, got, []uint64{0x1FC0}, "only the in-page candidate survives")
	})

	t.Run("backward stride crossing the boundary yields nothing", func(t *testing.T) {
		const stream StreamID = 2

		p.Access(stream, 0x2080)
		p.Access(stream, 0x2040) // stride -0x40

		got := p.Access(stream, 0x2000) // Steady; the very next step crosses back a page
		assertNilCandidates(t, got, "first lookahead step already crosses")
	})
}

// TestStridePrefetcher_MultipleIndependentStreams verifies that two
// streams are trained independently and do not interfere with each other.
func TestStridePrefetcher_MultipleIndependentStreams(t *testing.T) {
	p := NewStridePrefetcher(baseConfig())
	const streamA, streamB StreamID = 1, 2

	p.Access(streamA, 0x1000)
	p.Access(streamB, 0x9000)

	p.Access(streamA, 0x1040) // stride +0x40
	p.Access(streamB, 0x9080) // stride +0x80

	gotA := p.Access(streamA, 0x1080)
	gotB := p.Access(streamB, 0x9100)

	assertCandidates(t, gotA, []uint64{0x10C0, 0x1100, 0x1140}, "stream A")
	assertCandidates(t, gotB, []uint64{0x9180, 0x9200, 0x9280}, "stream B")
}

// TestStridePrefetcher_LRUEviction verifies stream-table capacity
// management: the least-recently-touched stream is evicted to make room,
// touching a stream protects it from eviction, and a stream that gets
// evicted re-learns entirely from scratch if it is accessed again.
func TestStridePrefetcher_LRUEviction(t *testing.T) {
	cfg := baseConfig()
	cfg.ConfidenceThreshold = 1
	cfg.Degree = 1
	cfg.MaxStreams = 2

	p := NewStridePrefetcher(cfg)
	const streamA, streamB, streamC StreamID = 1, 2, 3

	p.Access(streamA, 0x0000)
	p.Access(streamA, 0x0040) // A reaches Steady
	assertLookup(t, p, streamA, StateSteady, 1, true, "A steady before eviction")

	p.Access(streamB, 0x1000) // table = [A, B] (A is LRU)
	if p.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", p.Len())
	}

	p.Access(streamC, 0x2000) // table at capacity: evicts A, table = [B, C]
	if p.Len() != 2 {
		t.Fatalf("Len() after eviction = %d, want 2", p.Len())
	}

	assertLookup(t, p, streamA, StateInitial, 0, false, "A evicted")
	assertLookup(t, p, streamB, StateInitial, 0, true, "B survives")
	assertLookup(t, p, streamC, StateInitial, 0, true, "C survives")

	// Re-accessing the evicted stream A allocates it fresh (Initial, not
	// its old Steady state), and — being new while the table is again at
	// capacity — evicts B, the current LRU.
	p.Access(streamA, 0x5000)
	assertLookup(t, p, streamA, StateInitial, 0, true, "A relearns from scratch")
	assertLookup(t, p, streamB, StateInitial, 0, false, "B evicted in turn")
	assertLookup(t, p, streamC, StateInitial, 0, true, "C still tracked")
}

// TestStridePrefetcher_LRUEviction_TouchProtectsFromEviction verifies that
// accessing a stream again — not just creating it — refreshes its recency.
func TestStridePrefetcher_LRUEviction_TouchProtectsFromEviction(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxStreams = 2

	p := NewStridePrefetcher(cfg)
	const streamA, streamB, streamC StreamID = 1, 2, 3

	p.Access(streamA, 0x0000) // table = [A]
	p.Access(streamB, 0x1000) // table = [A, B]
	p.Access(streamA, 0x0040) // touch A again: table = [B, A]

	p.Access(streamC, 0x2000) // evicts B, the now-LRU stream

	assertLookup(t, p, streamB, StateInitial, 0, false, "B evicted")
	assertLookup(t, p, streamA, StateTransient, 1, true, "A retained")
	assertLookup(t, p, streamC, StateInitial, 0, true, "C tracked")
}

// TestStridePrefetcher_Reset verifies that Reset clears both the stream
// table and each stream's learned confidence.
func TestStridePrefetcher_Reset(t *testing.T) {
	cfg := baseConfig()
	cfg.ConfidenceThreshold = 1

	p := NewStridePrefetcher(cfg)
	const stream StreamID = 1

	p.Access(stream, 0x1000)
	p.Access(stream, 0x1040) // reaches Steady

	if p.Len() != 1 {
		t.Fatalf("Len() before reset = %d, want 1", p.Len())
	}

	p.Reset()

	if p.Len() != 0 {
		t.Fatalf("Len() after reset = %d, want 0", p.Len())
	}

	assertLookup(t, p, stream, StateInitial, 0, false, "untracked after reset")

	// A fresh access after Reset must start learning from scratch, not
	// resume the pre-reset Steady state.
	p.Access(stream, 0x9000)
	assertLookup(t, p, stream, StateInitial, 0, true, "restarts from Initial")
}

// TestNewStridePrefetcher_PanicsOnInvalidConfig verifies that the
// constructor rejects configurations that could never predict or track
// anything.
func TestNewStridePrefetcher_PanicsOnInvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(cfg *StrideConfig)
		wantPanic bool
	}{
		{
			name:      "valid config does not panic",
			mutate:    func(cfg *StrideConfig) {},
			wantPanic: false,
		},
		{
			name:      "zero ConfidenceThreshold panics",
			mutate:    func(cfg *StrideConfig) { cfg.ConfidenceThreshold = 0 },
			wantPanic: true,
		},
		{
			name:      "zero Degree panics",
			mutate:    func(cfg *StrideConfig) { cfg.Degree = 0 },
			wantPanic: true,
		},
		{
			name:      "negative Degree panics",
			mutate:    func(cfg *StrideConfig) { cfg.Degree = -1 },
			wantPanic: true,
		},
		{
			name:      "zero MaxStreams panics",
			mutate:    func(cfg *StrideConfig) { cfg.MaxStreams = 0 },
			wantPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			tt.mutate(&cfg)

			defer func() {
				r := recover()
				gotPanic := r != nil

				if gotPanic != tt.wantPanic {
					t.Errorf("panic = %v (recovered %v), want panic = %v", gotPanic, r, tt.wantPanic)
				}
			}()

			NewStridePrefetcher(cfg)
		})
	}
}

// TestStridePrefetcher_NegativeStride verifies that a descending access
// pattern is learned and predicted just as a positive one is.
func TestStridePrefetcher_NegativeStride(t *testing.T) {
	p := NewStridePrefetcher(baseConfig())
	const stream StreamID = 1

	p.Access(stream, 0x3000)
	p.Access(stream, 0x2FC0) // stride -0x40

	got := p.Access(stream, 0x2F80) // Steady
	assertCandidates(t, got, []uint64{0x2F40, 0x2F00, 0x2EC0}, "descending lookahead")
}

// TestStridePrefetcher_ImplementsPrefetcher is a compile-time-flavored
// sanity check that StridePrefetcher can be used through the Prefetcher
// interface, as it is intended to be plugged in behind.
func TestStridePrefetcher_ImplementsPrefetcher(t *testing.T) {
	var p Prefetcher = NewStridePrefetcher(baseConfig())

	if got := p.Access(1, 0x1000); got != nil {
		t.Fatalf("first access through interface: got %v, want nil", got)
	}

	p.Reset()
}

// TestState_String verifies the human-readable names used in test failure
// messages and debugging output.
func TestState_String(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{StateInitial, "Initial"},
		{StateTransient, "Transient"},
		{StateSteady, "Steady"},
		{State(99), "Unknown"},
	}

	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}
