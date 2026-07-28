package prefetch

import "container/list"

// State classifies a stream's confidence in its currently learned stride,
// following the classic three-state stride-prefetcher confidence machine.
type State int

const (
	// StateInitial means the stream has not yet observed enough accesses
	// to guess a stride: either it has never been accessed, or its last
	// access was the first one since creation or a reset.
	StateInitial State = iota

	// StateTransient means a stride has been guessed but has not repeated
	// enough consecutive times to reach StrideConfig.ConfidenceThreshold.
	// No prefetch candidates are generated in this state.
	StateTransient

	// StateSteady means the guessed stride has been confirmed at least
	// ConfidenceThreshold times in a row. The prefetcher actively
	// generates prefetch candidates while a stream is in this state.
	StateSteady
)

// String returns a human-readable name for s, primarily for test failure
// messages and debugging.
func (s State) String() string {
	switch s {
	case StateInitial:
		return "Initial"
	case StateTransient:
		return "Transient"
	case StateSteady:
		return "Steady"
	default:
		return "Unknown"
	}
}

// StrideConfig holds the immutable, user-tunable parameters of a
// StridePrefetcher.
type StrideConfig struct {
	// ConfidenceThreshold is the number of consecutive stride matches
	// required before a stream is trusted enough to generate prefetch
	// candidates (i.e., before it enters StateSteady). Must be >= 1.
	ConfidenceThreshold uint32

	// Degree is the number of strides to prefetch ahead once a stream
	// reaches StateSteady (the prefetcher's lookahead distance). Must be
	// >= 1.
	Degree int

	// Log2BlockSize is log2 of the cache block size, in bytes. Observed
	// addresses and generated candidates are aligned down to this
	// granularity before stride detection, so accesses within the same
	// block never register as a new stride.
	Log2BlockSize uint64

	// Log2PageSize is log2 of the page size, in bytes, of the address
	// space the prefetcher observes. A candidate that would land on a
	// different page than the access that triggered it is suppressed,
	// since a stride learned within one page says nothing about how the
	// next page is mapped.
	Log2PageSize uint64

	// MaxStreams caps the number of concurrently tracked streams. When a
	// stream that is not already in the table is accessed while the table
	// is at capacity, the least-recently-touched stream is evicted to
	// make room. Must be >= 1.
	MaxStreams int
}

// streamState is the per-stream learned state tracked by a
// StridePrefetcher. confidence == 0 is the sentinel for "no stride guessed
// yet" (StateInitial); it is otherwise never reset back to 0; a broken
// pattern instead adopts the newly observed delta at confidence 1 (see
// StridePrefetcher.Access).
type streamState struct {
	id         StreamID
	hasAddr    bool
	lastAddr   uint64
	lastStride int64
	confidence uint32
}

// state derives the streamState's State from its confidence counter and
// the owning prefetcher's ConfidenceThreshold.
func (s *streamState) state(threshold uint32) State {
	switch {
	case s.confidence == 0:
		return StateInitial
	case s.confidence < threshold:
		return StateTransient
	default:
		return StateSteady
	}
}

// StridePrefetcher implements Prefetcher using classic per-stream stride
// detection: it tracks the last address and stride observed on each
// stream, builds confidence as the same stride repeats on consecutive
// accesses, and — once confident (StateSteady) — predicts that the pattern
// continues, emitting up to Degree candidate addresses ahead of the
// triggering access. An access whose delta from the last one does not
// match the learned stride immediately unlearns the stream, dropping its
// confidence and adopting the just-observed delta as a fresh guess, rather
// than continuing to prefetch against a pattern that has already broken.
//
// StridePrefetcher is not safe for concurrent use.
type StridePrefetcher struct {
	cfg StrideConfig

	// order tracks stream recency: the front element is the
	// least-recently-touched stream (the next eviction victim) and the
	// back element is the most-recently-touched one. Each element's Value
	// is a *streamState.
	order   *list.List
	streams map[StreamID]*list.Element
}

// Compile-time assertion that StridePrefetcher satisfies Prefetcher.
var _ Prefetcher = (*StridePrefetcher)(nil)

// NewStridePrefetcher constructs a StridePrefetcher from cfg. It panics if
// cfg has a non-positive ConfidenceThreshold, Degree, or MaxStreams, since
// any of those would describe a prefetcher that could never usefully
// predict or track anything.
func NewStridePrefetcher(cfg StrideConfig) *StridePrefetcher {
	if cfg.ConfidenceThreshold == 0 {
		panic("prefetch: ConfidenceThreshold must be >= 1")
	}

	if cfg.Degree <= 0 {
		panic("prefetch: Degree must be >= 1")
	}

	if cfg.MaxStreams <= 0 {
		panic("prefetch: MaxStreams must be >= 1")
	}

	return &StridePrefetcher{
		cfg:     cfg,
		order:   list.New(),
		streams: make(map[StreamID]*list.Element),
	}
}

// Access implements Prefetcher.
func (p *StridePrefetcher) Access(stream StreamID, addr uint64) []uint64 {
	blockAddr := p.blockAlign(addr)
	s := p.touch(stream)

	if !s.hasAddr {
		s.hasAddr = true
		s.lastAddr = blockAddr

		return nil
	}

	// Subtract in the unsigned domain first (wraps mod 2^64, matching
	// two's-complement semantics exactly) and reinterpret the bit pattern
	// as signed once, rather than reinterpreting each operand up front —
	// this keeps the full-width address values out of the signed domain
	// entirely and confines the sign reinterpretation to the difference,
	// which is the only value that ever needs to be negative.
	delta := int64(blockAddr - s.lastAddr)
	s.lastAddr = blockAddr

	switch {
	case delta == 0:
		// A repeated access to the same block carries no stride
		// information, so it neither confirms nor breaks the learned
		// pattern.
	case s.confidence == 0:
		s.lastStride = delta
		s.confidence = 1
	case delta == s.lastStride:
		if s.confidence < p.cfg.ConfidenceThreshold {
			s.confidence++
		}
	default:
		// The access broke the learned pattern: unlearn it and start
		// over from this newly observed delta.
		s.lastStride = delta
		s.confidence = 1
	}

	if s.state(p.cfg.ConfidenceThreshold) != StateSteady {
		return nil
	}

	result := p.candidates(blockAddr, s.lastStride)
	if len(result) == 0 {
		// Steady, but every lookahead step crossed the page boundary: no
		// prediction is safe to return this access.
		return nil
	}

	return result
}

// Reset implements Prefetcher.
func (p *StridePrefetcher) Reset() {
	p.order = list.New()
	p.streams = make(map[StreamID]*list.Element)
}

// Len returns the number of streams currently tracked, for observing the
// stream table's occupancy and eviction behavior.
func (p *StridePrefetcher) Len() int {
	return p.order.Len()
}

// Lookup returns the current confidence State and raw confidence counter
// for stream, and whether the stream is currently tracked at all. It does
// not affect recency, so it never triggers eviction.
func (p *StridePrefetcher) Lookup(stream StreamID) (state State, confidence uint32, ok bool) {
	elem, found := p.streams[stream]
	if !found {
		return StateInitial, 0, false
	}

	s := elem.Value.(*streamState)

	return s.state(p.cfg.ConfidenceThreshold), s.confidence, true
}

// touch returns the streamState for stream, marking it most-recently-used.
// If stream is not yet tracked, it allocates a fresh entry, evicting the
// least-recently-touched stream first if the table is at capacity.
func (p *StridePrefetcher) touch(stream StreamID) *streamState {
	if elem, ok := p.streams[stream]; ok {
		p.order.MoveToBack(elem)

		return elem.Value.(*streamState)
	}

	if p.order.Len() >= p.cfg.MaxStreams {
		p.evictLRU()
	}

	s := &streamState{id: stream}
	elem := p.order.PushBack(s)
	p.streams[stream] = elem

	return s
}

// evictLRU removes the least-recently-touched stream from the table.
func (p *StridePrefetcher) evictLRU() {
	front := p.order.Front()
	if front == nil {
		return
	}

	victim := front.Value.(*streamState)
	p.order.Remove(front)
	delete(p.streams, victim.id)
}

// candidates generates up to Degree prefetch-candidate addresses ahead of
// triggerAddr along stride, stopping as soon as a candidate would cross
// onto a different page than triggerAddr. Since candidates advance
// monotonically by stride, once one candidate crosses the page boundary
// every subsequent one would too, so the loop can stop rather than merely
// skip.
func (p *StridePrefetcher) candidates(triggerAddr uint64, stride int64) []uint64 {
	triggerPage := triggerAddr >> p.cfg.Log2PageSize

	result := make([]uint64, 0, p.cfg.Degree)

	for i := 1; i <= p.cfg.Degree; i++ {
		// Only the offset (i*stride) is computed in the signed domain;
		// triggerAddr stays uint64 throughout and the offset is added back
		// via unsigned (wrapping) addition, the same reasoning as in
		// Access's delta computation.
		offset := uint64(int64(i) * stride)
		candidate := triggerAddr + offset
		if candidate>>p.cfg.Log2PageSize != triggerPage {
			break
		}

		result = append(result, candidate)
	}

	return result
}

// blockAlign rounds addr down to the enclosing cache block, per
// cfg.Log2BlockSize.
func (p *StridePrefetcher) blockAlign(addr uint64) uint64 {
	mask := uint64(1)<<p.cfg.Log2BlockSize - 1

	return addr &^ mask
}
