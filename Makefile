SHELL := /bin/bash

PROJECT := xen_exporter
VERSION ?= $(shell cat VERSION)
RELEASE ?= 1

DIST_DIR := dist
RPM_DIR := $(DIST_DIR)/rpm
SOURCES_DIR := $(RPM_DIR)/SOURCES
SPECS_DIR := $(RPM_DIR)/SPECS

TARBALL := $(PROJECT)-$(VERSION).tar.gz
TARBALL_PATH := $(SOURCES_DIR)/$(TARBALL)
SPEC := packaging/rpm/xen-exporter.spec

.PHONY: all build clean rpm-tree rpm-tarball rpm

all: build

build:
	CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o $(PROJECT) ./cmd/xen_exporter

clean:
	rm -rf $(DIST_DIR)
	rm -f $(PROJECT)

rpm-tree:
	mkdir -p $(RPM_DIR)/BUILD $(RPM_DIR)/BUILDROOT $(RPM_DIR)/RPMS $(RPM_DIR)/SRPMS $(SOURCES_DIR) $(SPECS_DIR)

rpm-tarball: rpm-tree
	@if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then \
		git archive --format=tar.gz --prefix=$(PROJECT)-$(VERSION)/ -o $(TARBALL_PATH) HEAD; \
	else \
		tmpdir=$$(mktemp -d); \
		mkdir -p $$tmpdir/$(PROJECT)-$(VERSION); \
		rsync -a --exclude .git --exclude dist ./ $$tmpdir/$(PROJECT)-$(VERSION)/; \
		tar -C $$tmpdir -czf $(TARBALL_PATH) $(PROJECT)-$(VERSION); \
		rm -rf $$tmpdir; \
	fi
	@echo "Created $(TARBALL_PATH)"

rpm: rpm-tarball
	cp -f $(SPEC) $(SPECS_DIR)/
	rpmbuild -ba $(SPECS_DIR)/$$(basename $(SPEC)) \
		--define "_topdir $(abspath $(RPM_DIR))" \
		--define "version $(VERSION)" \
		--define "release $(RELEASE)"
	@echo "RPM artifacts are under $(RPM_DIR)/RPMS and $(RPM_DIR)/SRPMS"
