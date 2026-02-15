SHELL := /bin/bash

GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0

DIST := dist
ARTIFACT_PREFIX ?= chaosforge

LAMBDA_PKGS := \
	services/api/cmd:api \
	services/functions/planner/cmd:planner \
	services/functions/alarmchecker/cmd:alarmchecker \
	services/functions/approvalwaiter/cmd:approvalwaiter \
	services/functions/pluginexecutor/cmd:pluginexecutor \
	services/functions/monitor/cmd:monitor \
	services/functions/reporter/cmd:reporter \
	services/functions/abort/cmd:abort

PLUGIN_PKGS := \
	services/plugins/ec2-stop/cmd:plugin-ec2-stop \
	services/plugins/ec2-terminate/cmd:plugin-ec2-terminate \
	services/plugins/ssm-netem/cmd:plugin-ssm-netem \
	services/plugins/ssm-stressng/cmd:plugin-ssm-stressng \
	services/plugins/alb-blackhole/cmd:plugin-alb-blackhole

.PHONY: all clean tidy build package tools

all: package

clean:
	rm -rf $(DIST)

tidy:
	go mod tidy

build:
	@mkdir -p $(DIST)
	@echo "Building lambdas ($(GOOS)/$(GOARCH))..."
	@for item in $(LAMBDA_PKGS) $(PLUGIN_PKGS); do \
		pkg=$${item%%:*}; name=$${item##*:}; \
		outdir=$(DIST)/$$name; mkdir -p $$outdir; \
		echo "  - $$name"; \
		CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "-s -w" -o $$outdir/main ./$$pkg; \
	done

package: build
	@echo "Zipping artifacts..."
	@for d in $(DIST)/*; do \
		name=$$(basename $$d); \
		( cd $$d && zip -q -r ../$$name.zip main ); \
	done
	@echo "Artifacts in $(DIST):"; ls -1 $(DIST)/*.zip | sed 's/^/  - /'

tools:
	@mkdir -p bin
	@echo "Building CLI tools..."
	GOOS=$$(go env GOOS) GOARCH=$$(go env GOARCH) CGO_ENABLED=$(CGO_ENABLED) go build -trimpath -ldflags "-s -w" -o bin/chaosforge ./cmd/chaosforge
	GOOS=$$(go env GOOS) GOARCH=$$(go env GOARCH) CGO_ENABLED=$(CGO_ENABLED) go build -trimpath -ldflags "-s -w" -o bin/chaosctl ./cmd/chaosctl
	@echo "  - bin/chaosforge"
	@echo "  - bin/chaosctl"

