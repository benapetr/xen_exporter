# xen_exporter

`xen_exporter` is a lightweight Prometheus exporter for Xen dom0 hosts.

It is designed to mirror key xapi `xcp-rrdd-cpu` semantics without consuming RRD, and uses low-level access directly:

- Host physical CPU usage (`xen_host_cpu_usage_ratio`, `xen_host_cpu_avg_usage_ratio`)
- Domain CPU usage and CPU seconds (`xen_domain_cpu_usage_ratio`, `xen_domain_cpu_seconds_total`) via `libxenctrl`
- Host load/running counts (`xen_hostload_ratio`, `xen_host_running_vcpus`, `xen_host_running_domains`)

## Efficiency model

Collectors run in background loops and cache results. Scrapes only serialize cached samples.

- `libxenctrl` collector: default every `5s`

This keeps scrape latency and CPU overhead low.

## Build

For EL7 targets, use Go **1.17** (last Go release supporting EL7/glibc baseline).

```bash
CGO_ENABLED=1 go build -o xen_exporter ./cmd/xen_exporter
```

Note: RPM spec does not enforce a distro `golang` package dependency at build
time, so external Go toolchains (for example tarball-installed Go 1.17) work.

## Build `libxenctrl` from Xen source (minimal libs-only path)

Use this when your dom0 image does not provide `xen-devel` packages, or they are incomplete.

```bash
# 1) Get Xen sources
git clone https://github.com/xen-project/xen.git
cd xen

# 2) Configure Xen build with firmware/qemu paths disabled
make distclean
./configure \
  --disable-xen \
  --disable-docs \
  --disable-monitors \
  --disable-ocamltools \
  --disable-golang \
  --disable-seabios \
  --disable-ovmf \
  --disable-ipxe \
  --disable-rombios \
  --with-system-qemu=/usr/bin/qemu-system-i386

# 3) Build only toolstack libraries (includes libxenctrl)
make -C tools/libs -j"$(nproc)"

# 4) Install only libraries and headers
sudo make -C tools/libs install
```

Optional staged install instead of writing to `/usr/local`:

```bash
make -C tools/libs install DESTDIR=/tmp/xen-libs-stage
```

After installation, verify:

```bash
ls /usr/local/include/xenctrl.h
ls /usr/local/lib*/libxenctrl.so*
```

Then build `xen_exporter`:

```bash
cd /path/to/xen_exporter
CGO_ENABLED=1 go build -o xen_exporter ./cmd/xen_exporter
```

## RPM packaging

The repository includes RPM packaging files under `packaging/rpm/`.

```bash
# Build binary only
make build

# Create source tarball for rpmbuild
make rpm-tarball

# Build binary RPM + SRPM into dist/rpm/
make rpm
```

Alternative wrapper:

```bash
packaging/rpm/build-rpm.sh
```

## Run

```bash
./xen_exporter \
  --web.listen-address=:9120 \
  --collector.xenctrl.interval=5s
```

## Flags

- `--web.listen-address` (default `:9120`)
- `--web.metrics-path` (default `/metrics`)
- `--collector.xenctrl.interval` (default `5s`)

## Runtime requirements on dom0

- Xen headers and libraries for build: `xenctrl.h`, `libxenctrl`, `libxenstore` (development packages)
- sufficient privileges to query Xen domain stats (root recommended)

## Endpoints

- metrics: `/metrics`
- health: `/-/healthy`

## Systemd (EL7-style)

The RPM installs:

- unit file: `/usr/lib/systemd/system/xen_exporter.service`
- environment file: `/etc/sysconfig/xen_exporter`

Adjust runtime flags in `/etc/sysconfig/xen_exporter` via `OPTIONS=\"...\"`, then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now xen_exporter
sudo systemctl status xen_exporter
```

## License

This project is licensed under **GNU General Public License v3.0 (GPLv3)**.
See [LICENSE](LICENSE).
