//go:build linux && cgo
// +build linux,cgo

package collectors

/*
#cgo LDFLAGS: -lxenctrl
#include <stdlib.h>
#include <stdint.h>
#include <string.h>
#include <limits.h>

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

typedef struct {
    uint32_t cpu;
    uint64_t idletime_ns;
} xen_pcpu_sample;

static int xen_collect_all(
    xen_domain_sample **domains_out, int *domains_count, int *nr_cpus,
    xen_pcpu_sample **pcpus_out, int *pcpus_count) {
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

    xen_domain_sample *domain_samples = calloc((size_t)n, sizeof(xen_domain_sample));
    if (!domain_samples) {
        free(infos);
        xc_interface_close(xch);
        return -5;
    }

    for (i = 0; i < n; i++) {
        uint32_t v;
        uint32_t runnable = 0;
        domain_samples[i].domid = infos[i].domain;
        domain_samples[i].cpu_time_ns = infos[i].cpu_time;
        domain_samples[i].max_vcpu_id = infos[i].max_vcpu_id;
        domain_samples[i].nr_online_vcpus = infos[i].nr_online_vcpus;
        domain_samples[i].flags = infos[i].flags;
        memcpy(domain_samples[i].handle, &infos[i].handle, sizeof(domain_samples[i].handle));

        // Some domains can report an invalid/sentinel max_vcpu_id (for example
        // UINT_MAX). Keep iteration bounded to avoid wraparound/pathological
        // loops in the collector.
        if (infos[i].nr_online_vcpus > 0 &&
            infos[i].max_vcpu_id != UINT_MAX &&
            infos[i].max_vcpu_id < 4096) {
            for (v = 0; v <= infos[i].max_vcpu_id; v++) {
                xc_vcpuinfo_t vinfo;
                if (xc_vcpu_getinfo(xch, infos[i].domain, (int)v, &vinfo) == 0) {
                    if (vinfo.online && !vinfo.blocked) {
                        runnable++;
                    }
                }
            }
        }
        domain_samples[i].runnable_vcpus = runnable;
    }

    int max_cpus = *nr_cpus;
    xc_cpuinfo_t *cpuinfos = calloc((size_t)max_cpus, sizeof(xc_cpuinfo_t));
    if (!cpuinfos) {
        free(domain_samples);
        free(infos);
        xc_interface_close(xch);
        return -6;
    }

    int got_cpus = max_cpus;
    if (xc_getcpuinfo(xch, max_cpus, cpuinfos, &got_cpus) != 0) {
        free(cpuinfos);
        free(domain_samples);
        free(infos);
        xc_interface_close(xch);
        return -7;
    }
    if (got_cpus < 0) {
        got_cpus = 0;
    } else if (got_cpus > max_cpus) {
        got_cpus = max_cpus;
    }

    xen_pcpu_sample *pcpu_samples = calloc((size_t)got_cpus, sizeof(xen_pcpu_sample));
    if (!pcpu_samples) {
        free(cpuinfos);
        free(domain_samples);
        free(infos);
        xc_interface_close(xch);
        return -8;
    }

    for (i = 0; i < got_cpus; i++) {
        pcpu_samples[i].cpu = (uint32_t)i;
        pcpu_samples[i].idletime_ns = cpuinfos[i].idletime;
    }

    free(cpuinfos);
    free(infos);
    xc_interface_close(xch);
    *domains_out = domain_samples;
    *domains_count = n;
    *pcpus_out = pcpu_samples;
    *pcpus_count = got_cpus;
    return 0;
}

static void xen_free_domains(xen_domain_sample *samples) {
    free(samples);
}

static void xen_free_pcpus(xen_pcpu_sample *samples) {
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
	prevDom  map[string]domainPrev
	prevPCPU map[uint32]pcpuPrev
}

type domainPrev struct {
	time        time.Time
	cpuTimeNs   uint64
	onlineVcpus uint32
}

type pcpuPrev struct {
	time       time.Time
	idleTimeNs uint64
}

func NewXenctrlCollector(interval time.Duration) *XenctrlCollector {
	return &XenctrlCollector{
		interval: interval,
		prevDom:  make(map[string]domainPrev, 1024),
		prevPCPU: make(map[uint32]pcpuPrev, 256),
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
	var domPtr *C.xen_domain_sample
	var domCount C.int
	var nrCPUs C.int
	var pcpuPtr *C.xen_pcpu_sample
	var pcpuCount C.int

	rc := C.xen_collect_all(&domPtr, &domCount, &nrCPUs, &pcpuPtr, &pcpuCount)
	if rc != 0 {
		return nil, fmt.Errorf("xen_collect_all failed: %d", int(rc))
	}
	defer C.xen_free_domains(domPtr)
	defer C.xen_free_pcpus(pcpuPtr)

	n := int(domCount)
	pn := int(pcpuCount)
	now := time.Now()
	rows := unsafe.Slice((*C.xen_domain_sample)(unsafe.Pointer(domPtr)), n)
	pcpuRows := unsafe.Slice((*C.xen_pcpu_sample)(unsafe.Pointer(pcpuPtr)), pn)

	samples := make([]metrics.Sample, 0, n*4+pn+16)
	nextPrev := make(map[string]domainPrev, n)
	nextPrevPCPU := make(map[uint32]pcpuPrev, pn)

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

		if prev, ok := c.prevDom[uuid]; ok {
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

	var sumUsage float64
	var usedPCPUs int
	for i := 0; i < pn; i++ {
		row := pcpuRows[i]
		cpuID := uint32(row.cpu)
		labels := map[string]string{"cpu": fmt.Sprintf("%d", cpuID)}

		if prev, ok := c.prevPCPU[cpuID]; ok {
			if uint64(row.idletime_ns) >= prev.idleTimeNs {
				dt := now.Sub(prev.time).Seconds()
				if dt > 0 {
					dIdle := float64(uint64(row.idletime_ns)-prev.idleTimeNs) / 1e9
					u := 1.0 - (dIdle / dt)
					u = math.Max(0, math.Min(1, u))
					sumUsage += u
					usedPCPUs++
					samples = append(samples, metrics.Sample{
						Name:   "xen_host_cpu_usage_ratio",
						Help:   "Physical CPU usage ratio per CPU from Xen idletime counters; semantics align with xcp-rrdd-cpu cpuN.",
						Type:   metrics.Gauge,
						Value:  u,
						Labels: labels,
					})
				}
			}
		}

		nextPrevPCPU[cpuID] = pcpuPrev{time: now, idleTimeNs: uint64(row.idletime_ns)}
	}

	if usedPCPUs > 0 {
		samples = append(samples, metrics.Sample{
			Name:  "xen_host_cpu_avg_usage_ratio",
			Help:  "Average physical CPU usage ratio from Xen idletime counters; semantics align with xcp-rrdd-cpu cpu_avg.",
			Type:  metrics.Gauge,
			Value: sumUsage / float64(usedPCPUs),
		})
	}

	c.prevDom = nextPrev
	c.prevPCPU = nextPrevPCPU

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
