package prefetch

// StreamID identifies an independent address stream that a Prefetcher
// trains and predicts against. A caller derives it however suits its
// integration — for example, uint64(pid) for one stream per process, or a
// finer-grained key (such as a PID/PC pair packed into a single value) for
// per-instruction streams.
type StreamID uint64

// Prefetcher is the strategy interface implemented by every prefetching
// algorithm pluggable into this package (e.g., StridePrefetcher, and a
// future next-line prefetcher). It lets the writethroughcache — or any
// other caller — swap prefetching strategies behind a single control flag
// without depending on any particular algorithm's internals.
type Prefetcher interface {
	// Access records an observed memory access to addr on the given
	// stream, updates the algorithm's learned state for that stream, and
	// returns the addresses (if any) the algorithm now predicts should be
	// prefetched, ordered nearest-first. It returns nil when the algorithm
	// does not have — or no longer has — a confident prediction for the
	// stream.
	//
	// Access does not consult a cache or MSHR: the returned candidates are
	// pure predictions, and it is the caller's responsibility to check
	// whether a candidate is already resident (or already in flight)
	// before issuing an actual prefetch request for it.
	Access(stream StreamID, addr uint64) []uint64

	// Reset discards all learned state for every stream, returning the
	// Prefetcher to its just-constructed condition.
	Reset()
}
