package collectors

import (
	"context"
	"sync"
	"time"

	"xen_exporter/internal/metrics"
)

type Collector interface {
	Name() string
	Start(ctx context.Context)
	Snapshot() []metrics.Sample
}

type state struct {
	mu       sync.RWMutex
	samples  []metrics.Sample
	success  float64
	duration float64
}

func (s *state) snapshotWithMeta(name string) []metrics.Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]metrics.Sample, 0, len(s.samples)+2)
	out = append(out, s.samples...)
	out = append(out,
		metrics.Sample{
			Name:   "xen_exporter_collector_success",
			Help:   "Whether a collector update succeeded.",
			Type:   metrics.Gauge,
			Value:  s.success,
			Labels: map[string]string{"collector": name},
		},
		metrics.Sample{
			Name:   "xen_exporter_collector_duration_seconds",
			Help:   "Collector update duration in seconds.",
			Type:   metrics.Gauge,
			Value:  s.duration,
			Labels: map[string]string{"collector": name},
		},
	)
	return out
}

func (s *state) set(samples []metrics.Sample, ok bool, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.samples = samples
	}
	if ok {
		s.success = 1
	} else {
		s.success = 0
	}
	s.duration = d.Seconds()
}
