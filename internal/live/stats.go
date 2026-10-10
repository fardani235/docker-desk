package live

import (
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
)

// Sample is one derived statistics reading for a container: percentages and
// rates computed from two consecutive raw samples.
type Sample struct {
	Time       time.Time
	CPUPercent float64 // 100 = one full core
	MemUsed    uint64  // usage minus reclaimable file cache, as `docker stats`
	MemLimit   uint64
	MemPercent float64
	NetRxRate  float64 // bytes per second
	NetTxRate  float64
	BlkRdRate  float64
	BlkWrRate  float64
	NetRx      uint64 // cumulative bytes
	NetTx      uint64
	BlkRead    uint64
	BlkWrite   uint64
	Pids       uint64
}

// Derive computes a Sample from two consecutive raw samples. ok is false when
// the pair cannot be used (same or reversed timestamps).
func Derive(prev, cur engine.RawStats) (s Sample, ok bool) {
	dt := cur.Read.Sub(prev.Read).Seconds()
	if dt <= 0 {
		return Sample{}, false
	}
	s.Time = cur.Read
	s.MemUsed = cur.MemUsage - cur.MemCache
	s.MemLimit = cur.MemLimit
	if cur.MemLimit > 0 {
		s.MemPercent = float64(s.MemUsed) / float64(cur.MemLimit) * 100
	}
	s.NetRx, s.NetTx, s.BlkRead, s.BlkWrite, s.Pids = cur.NetRx, cur.NetTx, cur.BlkRead, cur.BlkWrite, cur.Pids

	// Same formula as `docker stats`: the container's share of the host's
	// CPU time over the interval, scaled by the number of CPUs.
	cpuDelta := delta(prev.CPUTotal, cur.CPUTotal)
	sysDelta := delta(prev.CPUSystem, cur.CPUSystem)
	if sysDelta > 0 && cpuDelta > 0 {
		cpus := float64(cur.OnlineCPUs)
		if cpus == 0 {
			cpus = 1
		}
		s.CPUPercent = cpuDelta / sysDelta * cpus * 100
	}
	s.NetRxRate = delta(prev.NetRx, cur.NetRx) / dt
	s.NetTxRate = delta(prev.NetTx, cur.NetTx) / dt
	s.BlkRdRate = delta(prev.BlkRead, cur.BlkRead) / dt
	s.BlkWrRate = delta(prev.BlkWrite, cur.BlkWrite) / dt
	return s, true
}

// delta is cur-prev, or 0 when a counter went backwards (container restart).
func delta(prev, cur uint64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur - prev)
}
