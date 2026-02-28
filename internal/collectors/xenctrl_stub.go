//go:build !linux || !cgo

package collectors

import (
	"context"
	"time"

	"xen_exporter/internal/metrics"
)

type XenctrlCollector struct {
	interval time.Duration
	st       state
}

func NewXenctrlCollector(interval time.Duration) *XenctrlCollector {
	return &XenctrlCollector{interval: interval}
}

func (c *XenctrlCollector) Name() string { return "xenctrl" }

func (c *XenctrlCollector) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(c.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.st.set(nil, false, 0)
			}
		}
	}()
}

func (c *XenctrlCollector) Snapshot() []metrics.Sample { return c.st.snapshotWithMeta(c.Name()) }
