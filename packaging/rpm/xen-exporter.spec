Name:           xen-exporter
Version:        %{?version}%{!?version:0.1.0}
Release:        %{?release}%{!?release:1}%{?dist}
Summary:        Prometheus exporter for Xen dom0 metrics via libxenctrl

License:        GPLv3
URL:            https://github.com/your-org/xen_exporter
Source0:        xen_exporter-%{version}.tar.gz

BuildRequires:  gcc
BuildRequires:  systemd

%description
xen-exporter exposes Xen dom0 metrics in Prometheus format. It reads metrics
using low-level interfaces (libxenctrl and /proc/stat) and provides an HTTP
/metrics endpoint.

%prep
%autosetup -n xen_exporter-%{version}

%build
export GO111MODULE=on
export CGO_ENABLED=1
go build -trimpath -ldflags "-s -w" -o xen_exporter ./cmd/xen_exporter

%install
install -Dpm0755 xen_exporter %{buildroot}%{_bindir}/xen_exporter
install -Dpm0644 deploy/xen_exporter.service %{buildroot}%{_unitdir}/xen_exporter.service
install -Dpm0644 deploy/xen_exporter.sysconfig %{buildroot}%{_sysconfdir}/sysconfig/xen_exporter

%post
if [ $1 -eq 1 ] ; then
    /bin/systemctl daemon-reload >/dev/null 2>&1 || :
fi

%preun
if [ $1 -eq 0 ] ; then
    /bin/systemctl --no-reload disable xen_exporter.service >/dev/null 2>&1 || :
    /bin/systemctl stop xen_exporter.service >/dev/null 2>&1 || :
fi

%postun
/bin/systemctl daemon-reload >/dev/null 2>&1 || :
if [ $1 -ge 1 ] ; then
    /bin/systemctl try-restart xen_exporter.service >/dev/null 2>&1 || :
fi

%files
%{_bindir}/xen_exporter
%{_unitdir}/xen_exporter.service
%config(noreplace) %{_sysconfdir}/sysconfig/xen_exporter
%license LICENSE
%doc README.md

%changelog
* Sat Feb 28 2026 Petr Bena <petr@bena.rocks> - %{version}-%{release}
- Initial RPM packaging
