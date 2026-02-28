package collectors

import (
	"bufio"
	"context"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"xen_exporter/internal/metrics"
)

type ProcStatCollector struct {
	interval time.Duration
	st       state
	prev     map[string]cpuTimes
}

type cpuTimes struct {
	total uint64
	idle  uint64
}

func NewProcStatCollector(interval time.Duration) *ProcStatCollector {
	return &ProcStatCollector{
		interval: interval,
		prev:     make(map[string]cpuTimes, 64),
	}
}

func (c *ProcStatCollector) Name() string { return "proc_stat" }

func (c *ProcStatCollector) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(c.interval)
		defer t.Stop()
		c.collectOnce()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.collectOnce()
			}
		}
	}()
}

func (c *ProcStatCollector) Snapshot() []metrics.Sample { return c.st.snapshotWithMeta(c.Name()) }

func (c *ProcStatCollector) collectOnce() {
	started := time.Now()
	samples, err := c.read()
	c.st.set(samples, err == nil, time.Since(started))
}

func (c *ProcStatCollector) read() ([]metrics.Sample, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	next := make(map[string]cpuTimes, 64)
	samples := make([]metrics.Sample, 0, 128)

	var sum float64
	var used int

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		if len(line) < 4 || line[3] < '0' || line[3] > '9' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		cpu := strings.TrimPrefix(fields[0], "cpu")
		t, ok := parseCPU(fields[1:])
		if !ok {
			continue
		}
		next[cpu] = t
		if prev, exists := c.prev[cpu]; exists {
			dTotal := t.total - prev.total
			dIdle := t.idle - prev.idle
			if dTotal > 0 {
				u := 1.0 - float64(dIdle)/float64(dTotal)
				u = math.Max(0, math.Min(1, u))
				sum += u
				used++
				samples = append(samples, metrics.Sample{
					Name:   "xen_host_cpu_usage_ratio",
					Help:   "Physical CPU usage ratio per CPU; semantics align with xcp-rrdd-cpu cpuN.",
					Type:   metrics.Gauge,
					Value:  u,
					Labels: map[string]string{"cpu": cpu},
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if used > 0 {
		samples = append(samples, metrics.Sample{
			Name:  "xen_host_cpu_avg_usage_ratio",
			Help:  "Average physical CPU usage ratio; semantics align with xcp-rrdd-cpu cpu_avg.",
			Type:  metrics.Gauge,
			Value: sum / float64(used),
		})
	}

	samples = append(samples, metrics.Sample{
		Name:  "xen_host_pcpu_count",
		Help:  "Number of physical CPUs observed from /proc/stat.",
		Type:  metrics.Gauge,
		Value: float64(len(next)),
	})

	c.prev = next
	return samples, nil
}

func parseCPU(fields []string) (cpuTimes, bool) {
	vals := make([]uint64, 0, len(fields))
	for _, f := range fields {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return cpuTimes{}, false
		}
		vals = append(vals, v)
	}
	var total uint64
	for _, v := range vals {
		total += v
	}
	if len(vals) < 5 {
		return cpuTimes{}, false
	}
	idle := vals[3] + vals[4]
	return cpuTimes{total: total, idle: idle}, true
}
