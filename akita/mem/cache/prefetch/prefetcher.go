package prefetch

// StreamID identifies an independent address stream a Prefetcher trains
// and predicts against (e.g. uint64(pid), or a finer-grained key).
type StreamID uint64

// Prefetcher is the strategy interface every prefetching algorithm
// implements, letting a cache swap algorithms behind one control flag.
type Prefetcher interface {
	// Access records an access to addr on stream and returns predicted
	// addresses to prefetch, nearest-first, or nil if unconfident. It
	// doesn't check cache/MSHR residency — that's the caller's job.
	Access(stream StreamID, addr uint64) []uint64

	// Reset discards all learned state for every stream, returning the
	// Prefetcher to its just-constructed condition.
	Reset()
}
