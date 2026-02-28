package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"xen_exporter/internal/collectors"
	"xen_exporter/internal/metrics"
)

func main() {
	var (
		listenAddr       = flag.String("web.listen-address", ":9120", "Address to listen on")
		metricsPath      = flag.String("web.metrics-path", "/metrics", "Path under which to expose metrics")
		procInterval     = flag.Duration("collector.procstat.interval", time.Second, "Background collection interval for /proc/stat")
		xenctrlInterval  = flag.Duration("collector.xenctrl.interval", 5*time.Second, "Background collection interval for libxenctrl polling")
	)
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	allCollectors := []collectors.Collector{
		collectors.NewProcStatCollector(*procInterval),
		collectors.NewXenctrlCollector(*xenctrlInterval),
	}
	for _, c := range allCollectors {
		c.Start(ctx)
	}

	started := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc(*metricsPath, func(w http.ResponseWriter, r *http.Request) {
		samples := make([]metrics.Sample, 0, 256)
		for _, c := range allCollectors {
			samples = append(samples, c.Snapshot()...)
		}
		samples = append(samples, metrics.Sample{
			Name:  "xen_exporter_uptime_seconds",
			Help:  "Exporter uptime in seconds.",
			Type:  metrics.Gauge,
			Value: time.Since(started).Seconds(),
		})

		payload := metrics.FormatPrometheus(samples)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
	})
	mux.HandleFunc("/-/healthy", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Printf("xen_exporter listening on %s (metrics: %s)\n", *listenAddr, *metricsPath)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
