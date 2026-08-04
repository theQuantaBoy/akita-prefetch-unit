package writeback

import (
	"hash/fnv"

	"github.com/sarchlab/akita/v5/mem/cache/prefetch"
	"github.com/sarchlab/akita/v5/mem/vm"
	"github.com/sarchlab/akita/v5/messaging"
)

// newPrefetcher builds the prefetch.Prefetcher spec.PrefetchAlgorithm
// selects, or nil if spec.EnablePrefetch is false. It panics on an
// unrecognized algorithm name.
func newPrefetcher(spec Spec) prefetch.Prefetcher {
	if !spec.EnablePrefetch {
		return nil
	}

	switch spec.PrefetchAlgorithm {
	case "", "stride":
		return prefetch.NewStridePrefetcher(prefetch.StrideConfig{
			ConfidenceThreshold: spec.PrefetchStrideThreshold,
			Degree:              spec.PrefetchStrideDegree,
			Log2BlockSize:       spec.Log2BlockSize,
			Log2PageSize:        spec.PrefetchLog2PageSize,
			MaxStreams:          spec.PrefetchStrideMaxStreams,
		})
	case "nextline":
		return prefetch.NewNextLinePrefetcher(prefetch.NextLineConfig{
			Degree:        spec.PrefetchNextLineDegree,
			Log2BlockSize: spec.Log2BlockSize,
			Log2PageSize:  spec.PrefetchLog2PageSize,
		})
	default:
		panic("writeback: unknown PrefetchAlgorithm " + spec.PrefetchAlgorithm)
	}
}

// streamIDFor keys a stream by (pid, src): pid alone would lump an entire
// kernel's concurrent wavefronts into one noisy stride stream, so src (the
// requester's port, hashed via FNV-1a) disambiguates them.
func streamIDFor(pid vm.PID, src messaging.RemotePort) prefetch.StreamID {
	h := fnv.New32a()
	_, _ = h.Write([]byte(src))

	return prefetch.StreamID(uint64(pid)<<32 | uint64(h.Sum32()))
}
