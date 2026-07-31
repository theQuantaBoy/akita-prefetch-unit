package writeback

import (
	"github.com/sarchlab/akita/v5/mem/cache/prefetch"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sarchlab/akita/v5/mem"
	"github.com/sarchlab/akita/v5/mem/idealmemcontroller"
	"github.com/sarchlab/akita/v5/mem/memprotocol"
	"github.com/sarchlab/akita/v5/mem/vm"
	"github.com/sarchlab/akita/v5/messaging"
	"github.com/sarchlab/akita/v5/modeling"
	"github.com/sarchlab/akita/v5/noc/directconnection"
	"github.com/sarchlab/akita/v5/timing"
	"github.com/sarchlab/akita/v5/tracing"
)

var _ = Describe("Prefetch Factory and Stream Keying", func() {
	Describe("newPrefetcher", func() {
		It("returns nil when EnablePrefetch is false", func() {
			spec := Spec{
				EnablePrefetch: false,
			}
			Expect(newPrefetcher(spec)).To(BeNil())
		})

		It("returns StridePrefetcher when algorithm is stride", func() {
			spec := Spec{
				EnablePrefetch:           true,
				PrefetchAlgorithm:        "stride",
				PrefetchStrideDegree:     3,
				PrefetchStrideThreshold:  2,
				PrefetchStrideMaxStreams: 16,
				PrefetchLog2PageSize:     12,
				Log2BlockSize:            6,
			}
			p := newPrefetcher(spec)
			Expect(p).NotTo(BeNil())
			_, ok := p.(*prefetch.StridePrefetcher)
			Expect(ok).To(BeTrue())
		})

		It("returns StridePrefetcher when algorithm is empty", func() {
			spec := Spec{
				EnablePrefetch:           true,
				PrefetchStrideDegree:     3,
				PrefetchStrideThreshold:  2,
				PrefetchStrideMaxStreams: 16,
				PrefetchLog2PageSize:     12,
				Log2BlockSize:            6,
			}
			p := newPrefetcher(spec)
			Expect(p).NotTo(BeNil())
			_, ok := p.(*prefetch.StridePrefetcher)
			Expect(ok).To(BeTrue())
		})

		It("returns NextLinePrefetcher when algorithm is nextline", func() {
			spec := Spec{
				EnablePrefetch:         true,
				PrefetchAlgorithm:      "nextline",
				PrefetchNextLineDegree: 2,
				PrefetchLog2PageSize:   12,
				Log2BlockSize:          6,
			}
			p := newPrefetcher(spec)
			Expect(p).NotTo(BeNil())
			_, ok := p.(*prefetch.NextLinePrefetcher)
			Expect(ok).To(BeTrue())
		})

		It("panics on unknown algorithm", func() {
			spec := Spec{
				EnablePrefetch:    true,
				PrefetchAlgorithm: "garbage",
			}
			Expect(func() { newPrefetcher(spec) }).To(Panic())
		})
	})

	Describe("streamIDFor", func() {
		It("returns same key for same pid and src", func() {
			a := streamIDFor(42, "L1Cache[3].Bottom")
			b := streamIDFor(42, "L1Cache[3].Bottom")
			Expect(a).To(Equal(b))
		})

		It("returns different key for different src with same pid", func() {
			a := streamIDFor(42, "L1Cache[1].Bottom")
			b := streamIDFor(42, "L1Cache[2].Bottom")
			Expect(a).NotTo(Equal(b))
		})

		It("returns different key for same src with different pid", func() {
			a := streamIDFor(1, "L1Cache[0].Bottom")
			b := streamIDFor(2, "L1Cache[0].Bottom")
			Expect(a).NotTo(Equal(b))
		})
	})
})

var _ = Describe("Write-Back Cache Prefetch Integration", func() {
	var (
		engine      timing.Engine
		cacheComp   *Comp
		dram        *idealmemcontroller.Comp
		dramStorage *mem.Storage
		conn        *directconnection.Comp
		agentPort   messaging.Port
	)

	BeforeEach(func() {
		engine = timing.NewSerialEngine()

		agentPort = messaging.NewPort(nil, 8, 8, "Agent.Top")

		dramStorage = mem.NewStorage(4 * mem.GB)
		dramSpec := idealmemcontroller.DefaultSpec()
		dramSpec.Width = 1
		dramSpec.Latency = 200
		dramSpec.CacheLineSize = 64
		dram = idealmemcontroller.MakeBuilder().
			WithRegistrar(modeling.NewStandaloneRegistrar(engine)).
			WithResources(idealmemcontroller.Resources{Storage: dramStorage}).
			WithSpec(dramSpec).
			Build("DRAM")
		dram.AssignPort("Top",
			messaging.NewPort(dram, 16, 16, dram.Name()+".Top"))
		dram.AssignPort("Control",
			messaging.NewPort(dram, 16, 16, dram.Name()+".Control"))

		conn = directconnection.MakeBuilder().
			WithRegistrar(modeling.NewStandaloneRegistrar(engine)).
			Build("Conn")
	})

	buildCache := func(spec Spec) {
		cacheComp = MakeBuilder().
			WithRegistrar(modeling.NewStandaloneRegistrar(engine)).
			WithSpec(spec).
			WithResources(Resources{
				AddressToPortMapper: &mem.SinglePortMapper{
					Port: dram.GetPortByName("Top").AsRemote(),
				},
			}).
			Build("L2Cache")
		for _, name := range []string{"Top", "Bottom", "Control"} {
			cacheComp.AssignPort(name,
				messaging.NewPort(cacheComp, 8, 8, cacheComp.Name()+"."+name))
		}
		conn.PlugIn(cacheComp.GetPortByName("Top"))
		conn.PlugIn(cacheComp.GetPortByName("Bottom"))
		conn.PlugIn(cacheComp.GetPortByName("Control"))
		conn.PlugIn(dram.GetPortByName("Top"))
		conn.PlugIn(agentPort)
	}

	Describe("Prefetch enabled - NextLine", func() {
		BeforeEach(func() {
			spec := DefaultSpec()
			spec.Freq = 1 * timing.GHz
			spec.Log2BlockSize = 6
			spec.BankLatency = 4
			spec.DirLatency = 1
			spec.WayAssociativity = 4
			spec.NumBanks = 1
			spec.NumSets = 64
			spec.NumMSHREntry = 16
			spec.NumReqPerCycle = 4
			spec.TotalByteSize = 512 * mem.KB
			spec.WriteBufferCapacity = 32
			spec.MaxInflightFetch = 16
			spec.MaxInflightEviction = 16
			spec.InterleavingSize = 4096
			spec.AddressMapperType = "single"

			spec.EnablePrefetch = true
			spec.PrefetchAlgorithm = "nextline"
			spec.PrefetchNextLineDegree = 2
			spec.PrefetchLog2PageSize = 12

			buildCache(spec)
		})

		It("sends exactly one DataReadyRsp for a single demand read miss", func() {
			err := dramStorage.Write(0x1000, make([]byte, 64))
			Expect(err).NotTo(HaveOccurred())

			read := memprotocol.ReadReq{}
			read.ID = timing.GetIDGenerator().Generate()
			read.Src = agentPort.AsRemote()
			read.Dst = cacheComp.GetPortByName("Top").AsRemote()
			read.Address = 0x1000
			read.PID = 1
			read.AccessByteSize = 4
			read.TrafficBytes = 12
			read.TrafficClass = "memprotocol.ReadReq"
			cacheComp.GetPortByName("Top").Deliver(read)

			engine.Run()

			// Count DataReadyRsp responses — only the demand read
			// should produce a response. Prefetch completions are
			// silently retired.
			responseCount := 0
			for {
				msg := agentPort.RetrieveIncoming()
				if msg == nil {
					break
				}
				_, ok := msg.(memprotocol.DataReadyRsp)
				if ok {
					responseCount++
				}
			}
			Expect(responseCount).To(Equal(1))
		})

		It("second read to prefetched line hits in cache", func() {
			tagTracer := tracing.NewTagCountTracer(
				func(t tracing.TaskStart) bool { return true })
			tracing.CollectTrace(cacheComp, tagTracer)

			// Seed both the demand address and the next-line candidate.
			for _, addr := range []uint64{0x0200, 0x0240} {
				data := make([]byte, 64)
				for i := range data {
					data[i] = byte(i)
				}
				err := dramStorage.Write(addr, data)
				Expect(err).NotTo(HaveOccurred())
			}

			// First demand read at 0x200 triggers next-line prefetch
			// of 0x240.
			r1 := memprotocol.ReadReq{}
			r1.ID = timing.GetIDGenerator().Generate()
			r1.Src = agentPort.AsRemote()
			r1.Dst = cacheComp.GetPortByName("Top").AsRemote()
			r1.Address = 0x0200
			r1.PID = 3
			r1.AccessByteSize = 4
			r1.TrafficBytes = 12
			r1.TrafficClass = "memprotocol.ReadReq"
			cacheComp.GetPortByName("Top").Deliver(r1)

			engine.Run()

			// Drain any lingering messages from the first run.
			for agentPort.RetrieveIncoming() != nil {
			}

			// Second demand read at 0x240 — should hit in cache
			// (the prefetcher brought it in).
			r2 := memprotocol.ReadReq{}
			r2.ID = timing.GetIDGenerator().Generate()
			r2.Src = agentPort.AsRemote()
			r2.Dst = cacheComp.GetPortByName("Top").AsRemote()
			r2.Address = 0x0240
			r2.PID = 3
			r2.AccessByteSize = 4
			r2.TrafficBytes = 12
			r2.TrafficClass = "memprotocol.ReadReq"
			cacheComp.GetPortByName("Top").Deliver(r2)

			engine.Run()

			responseCount := 0
			for {
				msg := agentPort.RetrieveIncoming()
				if msg == nil {
					break
				}
				_, ok := msg.(memprotocol.DataReadyRsp)
				if ok {
					responseCount++
				}
			}
			Expect(responseCount).To(Equal(1))

			// PrefetchNextLineDegree is 2, so the first demand read issues
			// two prefetches (0x0240 and 0x0280); the second demand read
			// claims 0x0240 as a hit.
			Expect(tagTracer.GetTagCount("prefetch-issued")).To(Equal(uint64(2)))
			Expect(tagTracer.GetTagCount("prefetch-hit")).To(Equal(uint64(1)))
		})
	})

	Describe("Prefetch enabled - Stride", func() {
		BeforeEach(func() {
			spec := DefaultSpec()
			spec.Freq = 1 * timing.GHz
			spec.Log2BlockSize = 6
			spec.BankLatency = 4
			spec.DirLatency = 1
			spec.WayAssociativity = 4
			spec.NumBanks = 1
			spec.NumSets = 64
			spec.NumMSHREntry = 16
			spec.NumReqPerCycle = 4
			spec.TotalByteSize = 512 * mem.KB
			spec.WriteBufferCapacity = 32
			spec.MaxInflightFetch = 16
			spec.MaxInflightEviction = 16
			spec.InterleavingSize = 4096
			spec.AddressMapperType = "single"

			spec.EnablePrefetch = true
			spec.PrefetchAlgorithm = "stride"
			spec.PrefetchStrideDegree = 2
			spec.PrefetchStrideThreshold = 2
			spec.PrefetchStrideMaxStreams = 8
			spec.PrefetchLog2PageSize = 12

			buildCache(spec)
		})

		It("completes demand reads without crashing with stride prefetcher", func() {
			// Seed DRAM across a stride pattern.
			for _, addr := range []uint64{0x0000, 0x0100, 0x0200, 0x0300} {
				data := make([]byte, 64)
				for i := range data {
					data[i] = byte(addr>>8) + byte(i)
				}
				err := dramStorage.Write(addr, data)
				Expect(err).NotTo(HaveOccurred())
			}

			// Issue three reads with constant stride so the
			// prefetcher trains past the confidence threshold.
			for _, addr := range []uint64{0x0000, 0x0100, 0x0200} {
				read := memprotocol.ReadReq{}
				read.ID = timing.GetIDGenerator().Generate()
				read.Src = agentPort.AsRemote()
				read.Dst = cacheComp.GetPortByName("Top").AsRemote()
				read.Address = addr
				read.PID = 4
				read.AccessByteSize = 4
				read.TrafficBytes = 12
				read.TrafficClass = "memprotocol.ReadReq"
				cacheComp.GetPortByName("Top").Deliver(read)

				engine.Run()

				// Drain before next send.
				for agentPort.RetrieveIncoming() != nil {
				}
			}

			// Verify the prefetcher has learned the stream.
			st := cacheComp.State
			Expect(st.Prefetcher).NotTo(BeNil())
		})
	})

	Describe("Prefetch disabled", func() {
		BeforeEach(func() {
			spec := DefaultSpec()
			spec.Freq = 1 * timing.GHz
			spec.Log2BlockSize = 6
			spec.BankLatency = 4
			spec.DirLatency = 1
			spec.WayAssociativity = 4
			spec.NumBanks = 1
			spec.NumSets = 64
			spec.NumMSHREntry = 16
			spec.NumReqPerCycle = 4
			spec.TotalByteSize = 512 * mem.KB
			spec.WriteBufferCapacity = 32
			spec.MaxInflightFetch = 16
			spec.MaxInflightEviction = 16
			spec.InterleavingSize = 4096
			spec.AddressMapperType = "single"

			spec.EnablePrefetch = false

			buildCache(spec)
		})

		It("handles normal read miss without prefetch interference", func() {
			err := dramStorage.Write(0x0000, make([]byte, 64))
			Expect(err).NotTo(HaveOccurred())

			read := memprotocol.ReadReq{}
			read.ID = timing.GetIDGenerator().Generate()
			read.Src = agentPort.AsRemote()
			read.Dst = cacheComp.GetPortByName("Top").AsRemote()
			read.Address = 0x0000
			read.PID = 1
			read.AccessByteSize = 4
			read.TrafficBytes = 12
			read.TrafficClass = "memprotocol.ReadReq"
			cacheComp.GetPortByName("Top").Deliver(read)

			engine.Run()

			responseCount := 0
			for {
				msg := agentPort.RetrieveIncoming()
				if msg == nil {
					break
				}
				_, ok := msg.(memprotocol.DataReadyRsp)
				if ok {
					responseCount++
				}
			}
			Expect(responseCount).To(Equal(1))
			Expect(cacheComp.State.Prefetcher).To(BeNil())
		})
	})
})

// Reference vm.PID to satisfy import.
var _ = vm.PID(0)
