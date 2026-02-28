//go:build linux && cgo

package collectors

/*
#cgo LDFLAGS: -lxenctrl
#include <stdlib.h>
#include <stdint.h>
#include <string.h>

#include <xenctrl.h>
#include <xen/domctl.h>

typedef struct {
    uint32_t domid;
    uint64_t cpu_time_ns;
    uint32_t max_vcpu_id;
    uint32_t nr_online_vcpus;
    uint32_t flags;
    uint8_t handle[16];
    uint32_t runnable_vcpus;
} xen_domain_sample;

static int xen_collect_domains(xen_domain_sample **out, int *count, int *nr_cpus) {
    int i;
    xc_interface *xch = xc_interface_open(NULL, NULL, 0);
    if (!xch) {
        return -1;
    }

    xc_physinfo_t pinfo;
    if (xc_physinfo(xch, &pinfo) != 0) {
        xc_interface_close(xch);
        return -2;
    }
    *nr_cpus = (int)pinfo.nr_cpus;

    int cap = 2048;
    xc_domaininfo_t *infos = calloc((size_t)cap, sizeof(xc_domaininfo_t));
    if (!infos) {
        xc_interface_close(xch);
        return -3;
    }

    int n = xc_domain_getinfolist(xch, 0, cap, infos);
    if (n < 0) {
        free(infos);
        xc_interface_close(xch);
        return -4;
    }

    xen_domain_sample *samples = calloc((size_t)n, sizeof(xen_domain_sample));
    if (!samples) {
        free(infos);
        xc_interface_close(xch);
        return -5;
    }

    for (i = 0; i < n; i++) {
        uint32_t v;
        samples[i].domid = infos[i].domain;
        samples[i].cpu_time_ns = infos[i].cpu_time;
        samples[i].max_vcpu_id = infos[i].max_vcpu_id;
        samples[i].nr_online_vcpus = infos[i].nr_online_vcpus;
        samples[i].flags = infos[i].flags;
        memcpy(samples[i].handle, infos[i].handle, 16);

        uint32_t runnable = 0;
        for (v = 0; v <= infos[i].max_vcpu_id; v++) {
            xc_vcpuinfo_t vinfo;
            if (xc_vcpu_getinfo(xch, infos[i].domain, (int)v, &vinfo) == 0) {
                if (vinfo.online && !vinfo.blocked) {
                    runnable++;
                }
            }
        }
        samples[i].runnable_vcpus = runnable;
    }

    free(infos);
    xc_interface_close(xch);
    *out = samples;
    *count = n;
    return 0;
}

static void xen_free_domains(xen_domain_sample *samples) {
    free(samples);
}
*/
import "C"

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unsafe"

	"xen_exporter/internal/metrics"
)

type XenctrlCollector struct {
	interval time.Duration
	st       state
	prev     map[string]domainPrev
}

type domainPrev struct {
	time        time.Time
	cpuTimeNs   uint64
	onlineVcpus uint32
}

func NewXenctrlCollector(interval time.Duration) *XenctrlCollector {
	return &XenctrlCollector{
		interval: interval,
		prev:     make(map[string]domainPrev, 1024),
	}
}

func (c *XenctrlCollector) Name() string { return "xenctrl" }

func (c *XenctrlCollector) Start(ctx context.Context) {
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

func (c *XenctrlCollector) Snapshot() []metrics.Sample { return c.st.snapshotWithMeta(c.Name()) }

func (c *XenctrlCollector) collectOnce() {
	started := time.Now()
	samples, err := c.read()
	c.st.set(samples, err == nil, time.Since(started))
}

func (c *XenctrlCollector) read() ([]metrics.Sample, error) {
	var ptr *C.xen_domain_sample
	var count C.int
	var nrCPUs C.int

	rc := C.xen_collect_domains(&ptr, &count, &nrCPUs)
	if rc != 0 {
		return nil, fmt.Errorf("xen_collect_domains failed: %d", int(rc))
	}
	defer C.xen_free_domains(ptr)

	n := int(count)
	now := time.Now()
	rows := unsafe.Slice((*C.xen_domain_sample)(unsafe.Pointer(ptr)), n)

	samples := make([]metrics.Sample, 0, n*4+8)
	nextPrev := make(map[string]domainPrev, n)

	runningDomains := 0
	runningVcpus := 0

	for i := 0; i < n; i++ {
		r := rows[i]
		uuid := uuidFromHandle(r.handle)
		if shouldSkipUUID(uuid) {
			continue
		}

		domid := fmt.Sprintf("%d", uint32(r.domid))
		labels := map[string]string{"domid": domid, "uuid": uuid}

		samples = append(samples,
			metrics.Sample{
				Name:   "xen_domain_cpu_seconds_total",
				Help:   "Total domain CPU time in seconds from libxenctrl domain info.",
				Type:   metrics.Counter,
				Value:  float64(uint64(r.cpu_time_ns)) / 1e9,
				Labels: labels,
			},
			metrics.Sample{
				Name:   "xen_domain_online_vcpus",
				Help:   "Online vCPUs for domain from libxenctrl.",
				Type:   metrics.Gauge,
				Value:  float64(uint32(r.nr_online_vcpus)),
				Labels: labels,
			},
			metrics.Sample{
				Name:   "xen_domain_runnable_vcpus",
				Help:   "Runnable vCPUs for domain (online and not blocked), aligned with xcp-rrdd-cpu hostload counting.",
				Type:   metrics.Gauge,
				Value:  float64(uint32(r.runnable_vcpus)),
				Labels: labels,
			},
		)

		if prev, ok := c.prev[uuid]; ok {
			dt := now.Sub(prev.time).Seconds()
			if dt > 0 {
				dcpu := float64(uint64(r.cpu_time_ns)-prev.cpuTimeNs) / 1e9
				ov := float64(uint32(r.nr_online_vcpus))
				if ov < 1 {
					ov = 1
				}
				u := dcpu / (dt * ov)
				u = math.Max(0, math.Min(1, u))
				samples = append(samples, metrics.Sample{
					Name:   "xen_domain_cpu_usage_ratio",
					Help:   "Domain CPU usage ratio derived from libxenctrl cpu_time; semantics align with xcp-rrdd-cpu cpu_usage.",
					Type:   metrics.Gauge,
					Value:  u,
					Labels: labels,
				})
			}
		}

		nextPrev[uuid] = domainPrev{time: now, cpuTimeNs: uint64(r.cpu_time_ns), onlineVcpus: uint32(r.nr_online_vcpus)}

		paused := (uint32(r.flags) & uint32(C.XEN_DOMINF_paused)) != 0
		if !paused {
			runningDomains++
		}
		runningVcpus += int(uint32(r.runnable_vcpus))
	}

	c.prev = nextPrev

	samples = append(samples,
		metrics.Sample{
			Name:  "xen_host_running_domains",
			Help:  "Total number of running domains from libxenctrl domain flags; semantics align with xcp-rrdd-cpu running_domains.",
			Type:  metrics.Gauge,
			Value: float64(runningDomains),
		},
		metrics.Sample{
			Name:  "xen_host_running_vcpus",
			Help:  "Total running/runnable vCPUs from libxenctrl vcpu info; semantics align with xcp-rrdd-cpu running_vcpus.",
			Type:  metrics.Gauge,
			Value: float64(runningVcpus),
		},
		metrics.Sample{
			Name:  "xen_host_pcpu_count_xen",
			Help:  "Physical CPU count from libxenctrl xc_physinfo.",
			Type:  metrics.Gauge,
			Value: float64(int(nrCPUs)),
		},
	)

	if int(nrCPUs) > 0 {
		samples = append(samples, metrics.Sample{
			Name:  "xen_hostload_ratio",
			Help:  "Host load per physical CPU from libxenctrl runnable vCPU counting; semantics align with xcp-rrdd-cpu hostload.",
			Type:  metrics.Gauge,
			Value: float64(runningVcpus) / float64(int(nrCPUs)),
		})
	}

	return samples, nil
}

func uuidFromHandle(h [16]C.uint8_t) string {
	b := make([]byte, 16)
	for i := 0; i < 16; i++ {
		b[i] = byte(h[i])
	}
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3],
		b[4], b[5],
		b[6], b[7],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15],
	)
}

func shouldSkipUUID(uuid string) bool {
	if strings.HasPrefix(uuid, "00000000-0000-0000") {
		return true
	}
	if strings.HasPrefix(uuid, "deadbeef-dead-beef") {
		return true
	}
	return false
}
