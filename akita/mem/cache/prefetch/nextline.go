package prefetch

// NextLineConfig holds the immutable, user-tunable parameters of a
// NextLinePrefetcher.
type NextLineConfig struct {
	// Degree is the number of sequential blocks to prefetch ahead of every
	// observed access. Must be >= 1.
	Degree int

	// Log2BlockSize is log2 of the cache block size, in bytes. The
	// observed address is aligned down to this granularity before the
	// sequential blocks ahead of it are computed.
	Log2BlockSize uint64

	// Log2PageSize is log2 of the page size, in bytes. A candidate that
	// would land on a different page than the triggering access is
	// suppressed, since the next block sequentially need not belong to
	// the same page-backed allocation.
	Log2PageSize uint64
}

// NextLinePrefetcher implements Prefetcher with the simplest possible
// strategy: every access unconditionally prefetches the Degree cache
// blocks immediately following it, regardless of stream or access
// history. Unlike StridePrefetcher it carries no per-stream learned state
// at all — stream is accepted only to satisfy the Prefetcher interface —
// so its prediction for a given address never depends on what was
// observed before it, and Reset is a no-op.
type NextLinePrefetcher struct {
	cfg NextLineConfig
}

// Compile-time assertion that NextLinePrefetcher satisfies Prefetcher.
var _ Prefetcher = (*NextLinePrefetcher)(nil)

// NewNextLinePrefetcher constructs a NextLinePrefetcher from cfg. It
// panics if cfg has a non-positive Degree, since that would describe a
// prefetcher that could never generate a candidate.
func NewNextLinePrefetcher(cfg NextLineConfig) *NextLinePrefetcher {
	if cfg.Degree <= 0 {
		panic("prefetch: Degree must be >= 1")
	}

	return &NextLinePrefetcher{cfg: cfg}
}

// Access implements Prefetcher. stream is unused: NextLinePrefetcher
// predicts identically regardless of which stream addr belongs to.
func (p *NextLinePrefetcher) Access(_ StreamID, addr uint64) []uint64 {
	blockAddr := p.blockAlign(addr)
	blockSize := uint64(1) << p.cfg.Log2BlockSize
	triggerPage := blockAddr >> p.cfg.Log2PageSize

	result := make([]uint64, 0, p.cfg.Degree)

	for i := 1; i <= p.cfg.Degree; i++ {
		candidate := blockAddr + uint64(i)*blockSize
		if candidate>>p.cfg.Log2PageSize != triggerPage {
			// Candidates advance monotonically forward, so once one
			// crosses the page boundary every subsequent one would too.
			break
		}

		result = append(result, candidate)
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

// Reset implements Prefetcher. NextLinePrefetcher carries no learned
// state, so there is nothing to discard.
func (p *NextLinePrefetcher) Reset() {}

// blockAlign rounds addr down to the enclosing cache block, per
// cfg.Log2BlockSize.
func (p *NextLinePrefetcher) blockAlign(addr uint64) uint64 {
	mask := uint64(1)<<p.cfg.Log2BlockSize - 1

	return addr &^ mask
}
