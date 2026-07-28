package writethroughcache

import (
	"hash/fnv"

	"github.com/sarchlab/akita/v5/mem/cache/prefetch"
	"github.com/sarchlab/akita/v5/mem/vm"
	"github.com/sarchlab/akita/v5/messaging"
)

// newPrefetcher builds the prefetch.Prefetcher spec.PrefetchAlgorithm
// selects, or nil if spec.PrefetchEnabled is false. It panics on an
// unrecognized algorithm name, mirroring how mem/cache/prefetch itself
// panics on an unusable StrideConfig.
func newPrefetcher(spec Spec) prefetch.Prefetcher {
	if !spec.PrefetchEnabled {
		return nil
	}

	switch spec.PrefetchAlgorithm {
	case "", "stride":
		return prefetch.NewStridePrefetcher(prefetch.StrideConfig{
			ConfidenceThreshold: spec.PrefetchConfidenceThreshold,
			Degree:              spec.PrefetchDegree,
			Log2BlockSize:       spec.Log2BlockSize,
			Log2PageSize:        spec.PrefetchLog2PageSize,
			MaxStreams:          spec.PrefetchMaxStreams,
		})
	case "nextline":
		return prefetch.NewNextLinePrefetcher(prefetch.NextLineConfig{
			Degree:        spec.PrefetchDegree,
			Log2BlockSize: spec.Log2BlockSize,
			Log2PageSize:  spec.PrefetchLog2PageSize,
		})
	default:
		panic("writethroughcache: unknown PrefetchAlgorithm " + spec.PrefetchAlgorithm)
	}
}

// streamIDFor derives a prefetch.StreamID that disambiguates concurrent
// requesters sharing one process: pid alone is not enough, since a whole
// GPU kernel launch (many concurrent wavefronts/compute units) typically
// runs under a single PID, and feeding their interleaved accesses into one
// shared stride stream would look like noise to the confidence FSM. src is
// the requester's port (preserved on the transaction from the original
// ReadReq's MsgMeta), which — unlike PID — actually distinguishes one
// upstream L1/compute unit from another.
//
// messaging.RemotePort is a plain string with no numeric identity, so it is
// folded into the key via FNV-1a rather than combined bitwise; a hash
// collision only degrades prediction accuracy (two requesters sharing a
// stream table entry), it can never affect correctness.
func streamIDFor(pid vm.PID, src messaging.RemotePort) prefetch.StreamID {
	h := fnv.New32a()
	_, _ = h.Write([]byte(src))

	return prefetch.StreamID(uint64(pid)<<32 | uint64(h.Sum32()))
}
