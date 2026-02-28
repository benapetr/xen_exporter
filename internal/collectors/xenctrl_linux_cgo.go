//go:build linux && cgo
// +build linux,cgo

package collectors

/*
#cgo CFLAGS: -std=gnu99
#cgo LDFLAGS: -lxenctrl -lxenstore
#include <stdlib.h>
#include <stdint.h>
#include <string.h>
#include <limits.h>
#include <stdio.h>

#include <xenctrl.h>
#include <xen/domctl.h>
#include <xenstore.h>

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

typedef struct {
    uint32_t cpu;
    uint32_t state;
    uint64_t residency_ns;
} xen_power_state_sample;

typedef struct {
    uint32_t cpu;
    uint32_t mhz;
} xen_freq_sample;

typedef struct {
    uint64_t total_kib;
    uint64_t free_kib;
    int64_t reclaimed_bytes;
    int64_t reclaimed_max_bytes;
    uint8_t has_reclaimed;
} xen_mem_sample;

static int read_xs_i64(struct xs_handle *xsh, const char *path, int64_t *out) {
    unsigned int len = 0;
    char *v = (char*)xs_read(xsh, XBT_NULL, path, &len);
    char *endp;
    long long parsed;

    if (!v) {
        return -1;
    }

    parsed = strtoll(v, &endp, 10);
    if (endp == v || (*endp != '\0' && *endp != '\n')) {
        free(v);
        return -1;
    }

    *out = (int64_t)parsed;
    free(v);
    return 0;
}

static int xen_collect_all(
    xen_domain_sample **domains_out, int *domains_count, int *nr_cpus,
    xen_pcpu_sample **pcpus_out, int *pcpus_count,
    xen_power_state_sample **px_out, int *px_count,
    xen_power_state_sample **cx_out, int *cx_count,
    xen_freq_sample **freq_out, int *freq_count,
    xen_mem_sample *mem_out) {
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
    mem_out->total_kib = ((uint64_t)pinfo.total_pages * (uint64_t)XC_PAGE_SIZE) / 1024ULL;
    mem_out->free_kib = ((uint64_t)pinfo.free_pages * (uint64_t)XC_PAGE_SIZE) / 1024ULL;
    mem_out->reclaimed_bytes = 0;
    mem_out->reclaimed_max_bytes = 0;
    mem_out->has_reclaimed = 0;

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
    if (n > 0 && !domain_samples) {
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

    {
        struct xs_handle *xsh = xs_open(0);
        if (xsh) {
            int64_t reclaimed_kib = 0;
            int64_t reclaimable_kib = 0;
            char p_max[128];
            char p_min[128];
            char p_target[128];

            for (i = 0; i < n; i++) {
                int64_t target;
                int64_t max;
                int64_t min;
                int have_target;
                int have_max;
                int have_min;

                snprintf(p_max, sizeof(p_max), "/local/domain/%u/memory/dynamic-max", infos[i].domain);
                snprintf(p_min, sizeof(p_min), "/local/domain/%u/memory/dynamic-min", infos[i].domain);
                snprintf(p_target, sizeof(p_target), "/local/domain/%u/memory/target", infos[i].domain);

                have_target = read_xs_i64(xsh, p_target, &target) == 0;
                have_max = read_xs_i64(xsh, p_max, &max) == 0;
                have_min = read_xs_i64(xsh, p_min, &min) == 0;

                if (have_target) {
                    if (have_max) {
                        reclaimed_kib += (max - target);
                    }
                    if (have_min) {
                        reclaimable_kib += (target - min);
                    }
                }
            }

            mem_out->reclaimed_bytes = reclaimed_kib * 1024;
            mem_out->reclaimed_max_bytes = reclaimable_kib * 1024;
            mem_out->has_reclaimed = 1;
            xs_close(xsh);
        }
    }

    {
        int max_cpus = *nr_cpus;
        int got_cpus = max_cpus;
        xc_cpuinfo_t *cpuinfos = calloc((size_t)max_cpus, sizeof(xc_cpuinfo_t));

        if (!cpuinfos) {
            free(domain_samples);
            free(infos);
            xc_interface_close(xch);
            return -6;
        }

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
        if (got_cpus > 0 && !pcpu_samples) {
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
        *pcpus_out = pcpu_samples;
        *pcpus_count = got_cpus;
    }

    {
        int cpu;
        int px_cap = 0;
        int cx_cap = 0;
        int fq_cap = *nr_cpus;
        int px_n = 0;
        int cx_n = 0;
        int fq_n = 0;
        xen_power_state_sample *px_samples;
        xen_power_state_sample *cx_samples;
        xen_freq_sample *fq_samples;

        for (cpu = 0; cpu < *nr_cpus; cpu++) {
            int max_px = 0;
            int max_cx = 0;
            if (xc_pm_get_max_px(xch, cpu, &max_px) == 0 && max_px > 0 && max_px < 128) {
                px_cap += max_px;
            }
            if (xc_pm_get_max_cx(xch, cpu, &max_cx) == 0 && max_cx > 0 && max_cx < 128) {
                cx_cap += max_cx;
            }
        }

        px_samples = calloc((size_t)px_cap, sizeof(xen_power_state_sample));
        cx_samples = calloc((size_t)cx_cap, sizeof(xen_power_state_sample));
        fq_samples = calloc((size_t)fq_cap, sizeof(xen_freq_sample));

        if ((px_cap > 0 && !px_samples) || (cx_cap > 0 && !cx_samples) || (fq_cap > 0 && !fq_samples)) {
            free(fq_samples);
            free(cx_samples);
            free(px_samples);
            free(domain_samples);
            free(infos);
            xc_interface_close(xch);
            return -9;
        }

        for (cpu = 0; cpu < *nr_cpus; cpu++) {
            int max_px = 0;
            int max_cx = 0;
            int avg_freq = 0;

            if (xc_get_cpufreq_avgfreq(xch, cpu, &avg_freq) == 0 && fq_n < fq_cap) {
                fq_samples[fq_n].cpu = (uint32_t)cpu;
                fq_samples[fq_n].mhz = (uint32_t)(avg_freq < 0 ? 0 : avg_freq);
                fq_n++;
            }

            if (xc_pm_get_max_px(xch, cpu, &max_px) == 0 && max_px > 0 && max_px < 128) {
                struct xc_px_stat pxs;
                memset(&pxs, 0, sizeof(pxs));
                pxs.total = (uint8_t)max_px;
                pxs.trans_pt = calloc((size_t)max_px * (size_t)max_px, sizeof(uint64_t));
                pxs.pt = calloc((size_t)max_px, sizeof(struct xc_px_val));

                if (pxs.trans_pt && pxs.pt && xc_pm_get_pxstat(xch, cpu, &pxs) == 0) {
                    int s;
                    int total = (int)pxs.total;
                    if (total > max_px) {
                        total = max_px;
                    }
                    for (s = 0; s < total && px_n < px_cap; s++) {
                        px_samples[px_n].cpu = (uint32_t)cpu;
                        px_samples[px_n].state = (uint32_t)s;
                        px_samples[px_n].residency_ns = pxs.pt[s].residency;
                        px_n++;
                    }
                }
                free(pxs.trans_pt);
                free(pxs.pt);
            }

            if (xc_pm_get_max_cx(xch, cpu, &max_cx) == 0 && max_cx > 0 && max_cx < 128) {
                struct xc_cx_stat cxs;
                memset(&cxs, 0, sizeof(cxs));
                cxs.nr = (uint32_t)max_cx;
                cxs.triggers = calloc((size_t)max_cx, sizeof(uint64_t));
                cxs.residencies = calloc((size_t)max_cx, sizeof(uint64_t));

                if (cxs.triggers && cxs.residencies && xc_pm_get_cxstat(xch, cpu, &cxs) == 0) {
                    int s;
                    int total = (int)cxs.nr;
                    if (total > max_cx) {
                        total = max_cx;
                    }
                    for (s = 0; s < total && cx_n < cx_cap; s++) {
                        cx_samples[cx_n].cpu = (uint32_t)cpu;
                        cx_samples[cx_n].state = (uint32_t)s;
                        cx_samples[cx_n].residency_ns = cxs.residencies[s];
                        cx_n++;
                    }
                }
                free(cxs.triggers);
                free(cxs.residencies);
                free(cxs.pc);
                free(cxs.cc);
            }
        }

        *px_out = px_samples;
        *px_count = px_n;
        *cx_out = cx_samples;
        *cx_count = cx_n;
        *freq_out = fq_samples;
        *freq_count = fq_n;
    }

    free(infos);
    xc_interface_close(xch);

    *domains_out = domain_samples;
    *domains_count = n;
    return 0;
}

static void xen_free_domains(xen_domain_sample *samples) {
    free(samples);
}

static void xen_free_pcpus(xen_pcpu_sample *samples) {
    free(samples);
}

static void xen_free_power_states(xen_power_state_sample *samples) {
    free(samples);
}

static void xen_free_freqs(xen_freq_sample *samples) {
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
	prevPM   map[string]pmPrev
}

type domainPrev struct {
	time      time.Time
	cpuTimeNs uint64
}

type pcpuPrev struct {
	time       time.Time
	idleTimeNs uint64
}

type pmPrev struct {
	time        time.Time
	residencyNs uint64
}

func NewXenctrlCollector(interval time.Duration) *XenctrlCollector {
	return &XenctrlCollector{
		interval: interval,
		prevDom:  make(map[string]domainPrev, 1024),
		prevPCPU: make(map[uint32]pcpuPrev, 256),
		prevPM:   make(map[string]pmPrev, 2048),
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
	var pxPtr *C.xen_power_state_sample
	var pxCount C.int
	var cxPtr *C.xen_power_state_sample
	var cxCount C.int
	var freqPtr *C.xen_freq_sample
	var freqCount C.int
	var mem C.xen_mem_sample

	rc := C.xen_collect_all(
		&domPtr,
		&domCount,
		&nrCPUs,
		&pcpuPtr,
		&pcpuCount,
		&pxPtr,
		&pxCount,
		&cxPtr,
		&cxCount,
		&freqPtr,
		&freqCount,
		&mem,
	)
	if rc != 0 {
		return nil, fmt.Errorf("xen_collect_all failed: %d", int(rc))
	}
	defer C.xen_free_domains(domPtr)
	defer C.xen_free_pcpus(pcpuPtr)
	defer C.xen_free_power_states(pxPtr)
	defer C.xen_free_power_states(cxPtr)
	defer C.xen_free_freqs(freqPtr)

	n := int(domCount)
	pn := int(pcpuCount)
	pxn := int(pxCount)
	cxn := int(cxCount)
	fqn := int(freqCount)
	now := time.Now()

	rows := unsafe.Slice((*C.xen_domain_sample)(unsafe.Pointer(domPtr)), n)
	pcpuRows := unsafe.Slice((*C.xen_pcpu_sample)(unsafe.Pointer(pcpuPtr)), pn)
	pxRows := unsafe.Slice((*C.xen_power_state_sample)(unsafe.Pointer(pxPtr)), pxn)
	cxRows := unsafe.Slice((*C.xen_power_state_sample)(unsafe.Pointer(cxPtr)), cxn)
	freqRows := unsafe.Slice((*C.xen_freq_sample)(unsafe.Pointer(freqPtr)), fqn)

	samples := make([]metrics.Sample, 0, n*4+pn+pxn+cxn+fqn+24)
	nextPrevDom := make(map[string]domainPrev, n)
	nextPrevPCPU := make(map[uint32]pcpuPrev, pn)
	nextPrevPM := make(map[string]pmPrev, pxn+cxn)

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

		nextPrevDom[uuid] = domainPrev{time: now, cpuTimeNs: uint64(r.cpu_time_ns)}

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

	for i := 0; i < fqn; i++ {
		row := freqRows[i]
		samples = append(samples, metrics.Sample{
			Name:   "xen_host_cpu_avg_frequency_mhz",
			Help:   "Average physical CPU frequency in MHz from Xen power-management stats.",
			Type:   metrics.Gauge,
			Value:  float64(uint32(row.mhz)),
			Labels: map[string]string{"cpu": fmt.Sprintf("%d", uint32(row.cpu))},
		})
	}

	for i := 0; i < pxn; i++ {
		row := pxRows[i]
		k := fmt.Sprintf("p:%d:%d", uint32(row.cpu), uint32(row.state))
		if prev, ok := c.prevPM[k]; ok && uint64(row.residency_ns) >= prev.residencyNs {
			dt := now.Sub(prev.time).Seconds()
			if dt > 0 {
				dres := float64(uint64(row.residency_ns)-prev.residencyNs) / 1e9
				u := dres / dt
				u = math.Max(0, math.Min(1, u))
				samples = append(samples, metrics.Sample{
					Name:  "xen_host_cpu_pstate_residency_ratio",
					Help:  "Proportion of time a physical CPU spent in a P-state from Xen PM residency counters.",
					Type:  metrics.Gauge,
					Value: u,
					Labels: map[string]string{
						"cpu":   fmt.Sprintf("%d", uint32(row.cpu)),
						"state": fmt.Sprintf("P%d", uint32(row.state)),
					},
				})
			}
		}
		nextPrevPM[k] = pmPrev{time: now, residencyNs: uint64(row.residency_ns)}
	}

	for i := 0; i < cxn; i++ {
		row := cxRows[i]
		k := fmt.Sprintf("c:%d:%d", uint32(row.cpu), uint32(row.state))
		if prev, ok := c.prevPM[k]; ok && uint64(row.residency_ns) >= prev.residencyNs {
			dt := now.Sub(prev.time).Seconds()
			if dt > 0 {
				dres := float64(uint64(row.residency_ns)-prev.residencyNs) / 1e9
				u := dres / dt
				u = math.Max(0, math.Min(1, u))
				samples = append(samples, metrics.Sample{
					Name:  "xen_host_cpu_cstate_residency_ratio",
					Help:  "Proportion of time a physical CPU spent in a C-state from Xen PM residency counters.",
					Type:  metrics.Gauge,
					Value: u,
					Labels: map[string]string{
						"cpu":   fmt.Sprintf("%d", uint32(row.cpu)),
						"state": fmt.Sprintf("C%d", uint32(row.state)),
					},
				})
			}
		}
		nextPrevPM[k] = pmPrev{time: now, residencyNs: uint64(row.residency_ns)}
	}

	samples = append(samples,
		metrics.Sample{
			Name:  "xen_host_memory_total_kib",
			Help:  "Total amount of memory on the Xen host in KiB (xc_physinfo total_pages).",
			Type:  metrics.Gauge,
			Value: float64(uint64(mem.total_kib)),
		},
		metrics.Sample{
			Name:  "xen_host_memory_free_kib",
			Help:  "Free memory on the Xen host in KiB (xc_physinfo free_pages).",
			Type:  metrics.Gauge,
			Value: float64(uint64(mem.free_kib)),
		},
	)
	if uint8(mem.has_reclaimed) != 0 {
		samples = append(samples,
			metrics.Sample{
				Name:  "xen_host_memory_reclaimed_bytes",
				Help:  "Host memory reclaimed by squeezing in bytes (sum of dynamic-max minus target across domains).",
				Type:  metrics.Gauge,
				Value: float64(int64(mem.reclaimed_bytes)),
			},
			metrics.Sample{
				Name:  "xen_host_memory_reclaimed_max_bytes",
				Help:  "Host memory that could be reclaimed by squeezing in bytes (sum of target minus dynamic-min across domains).",
				Type:  metrics.Gauge,
				Value: float64(int64(mem.reclaimed_max_bytes)),
			},
		)
	}

	c.prevDom = nextPrevDom
	c.prevPCPU = nextPrevPCPU
	c.prevPM = nextPrevPM

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
