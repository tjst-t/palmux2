.PHONY: dev dev-api dev-frontend ports tmp serve serve-stop serve-logs build build-linux build-arm build-agent test lint clean e2e-cleanup e2e-cleanup-dry

TMP_DIR := tmp
BIN_DIR := bin
GO_PKG := ./cmd/palmux

# `palmux port` is our built-in port allocator (S9fd775). It lives in the
# same binary as the server, so we can self-bootstrap from a freshly built
# `bin/palmux` without depending on the external `portman` CLI. The
# bootstrap order is:
#
#   1. Build the binary (a `dev` / `serve` target's first dependency).
#   2. Use the freshly built binary to allocate ports.
#   3. Use the same binary to run the server.
#
# All allocator state lives in $(PORTS_FILE) — a JSON file keyed on
# (scope, name). With the same name and scope you always get the same port
# back, mirroring portman's lease semantics.
PALMUX_BIN := $(BIN_DIR)/palmux
PORT_CONFIG_DIR := $(TMP_DIR)
PORTS_FILE := $(PORT_CONFIG_DIR)/ports.json

# Optional instance suffix so a second palmux2 (e.g. in a `dev` worktree)
# can run side-by-side with the host instance without sharing port leases.
# Default: blank → host instance ("palmux2", "palmux2-api",
# "palmux2-frontend"). Override with `make serve INSTANCE=dev` etc.
INSTANCE ?=
INSTANCE_SUFFIX := $(if $(INSTANCE),-$(INSTANCE),)
SERVE_NAME    := palmux2$(INSTANCE_SUFFIX)
API_NAME      := palmux2$(INSTANCE_SUFFIX)-api
FRONTEND_NAME := palmux2$(INSTANCE_SUFFIX)-frontend

# `palmux port alloc` returns the same port for the same (scope, name)
# combo, so the lease is stable across runs. Calling it multiple times in
# one Makefile run is safe.
PALMUX_PORT := $(PALMUX_BIN) port

tmp:
	@mkdir -p $(TMP_DIR)

# Pre-flight: make sure the palmux binary is built before any port
# allocation happens. We bootstrap with a plain `go build` (no frontend
# embed) so a fresh checkout works without `npm install`. Production
# `make build` rebuilds with frontend embed on top of this.
$(PALMUX_BIN): $(shell find cmd/palmux internal -type f -name '*.go' 2>/dev/null) prepare
	@mkdir -p $(BIN_DIR)
	@go build -o $(PALMUX_BIN) $(GO_PKG)

ports: $(PALMUX_BIN) tmp
	@$(PALMUX_PORT) alloc --config-dir $(PORT_CONFIG_DIR) --name $(API_NAME)      >/dev/null
	@$(PALMUX_PORT) alloc --config-dir $(PORT_CONFIG_DIR) --name $(FRONTEND_NAME) >/dev/null
	@echo "API_PORT=$$($(PALMUX_PORT) alloc --config-dir $(PORT_CONFIG_DIR) --name $(API_NAME))"
	@echo "FRONTEND_PORT=$$($(PALMUX_PORT) alloc --config-dir $(PORT_CONFIG_DIR) --name $(FRONTEND_NAME))"

dev: ports
	@$(MAKE) -j2 dev-api dev-frontend

dev-api: $(PALMUX_BIN) tmp
	@API_PORT=$$($(PALMUX_PORT) alloc --config-dir $(PORT_CONFIG_DIR) --name $(API_NAME)) && \
		go run $(GO_PKG) \
			--addr "0.0.0.0:$$API_PORT" \
			--config-dir ./$(TMP_DIR) \
			$(SERVE_TMUX_PREFIX)

dev-frontend: $(PALMUX_BIN) tmp
	@API_PORT=$$($(PALMUX_PORT) alloc --config-dir $(PORT_CONFIG_DIR) --name $(API_NAME)) && \
		FRONTEND_PORT=$$($(PALMUX_PORT) alloc --config-dir $(PORT_CONFIG_DIR) --name $(FRONTEND_NAME)) && \
		cd frontend && \
		PALMUX2_API_PORT=$$API_PORT \
		npm run dev -- --port $$FRONTEND_PORT --host 0.0.0.0 --strictPort

# Version string injected into the binary via `-ldflags -X main.Version=...`.
# Defaults to `git describe --tags --always --dirty` so a build from a tagged
# commit prints the tag (`v0.5.1`) and an in-progress build prints something
# like `v0.5.1-3-g0943bc8-dirty`. Override with `make build VERSION=...`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.Version=$(VERSION)

# Production: embed-built frontend, single binary
build: build-frontend
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/palmux $(GO_PKG)

build-frontend:
	cd frontend && npm run build
	@touch frontend/dist/.gitkeep

# Ensure frontend/dist exists with at least one file so `go build` works on a
# fresh clone before the frontend has been built. The placeholder is replaced
# the first time `make build` (or `npm run build`) runs.
prepare:
	@mkdir -p frontend/dist
	@[ -f frontend/dist/.gitkeep ] || touch frontend/dist/.gitkeep
	@[ -f frontend/dist/index.html ] || printf '<!doctype html><html><body><p>Run <code>make build</code> to bundle the frontend.</p></body></html>\n' > frontend/dist/index.html

build-linux: build-frontend
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/palmux-linux-amd64 $(GO_PKG)

build-arm: build-frontend
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/palmux-linux-arm64 $(GO_PKG)

# palmux-agent: in-container agent binary (static, no CGO, Linux amd64).
# AC-S98156b-1-1: must produce bin/palmux-agent ≤ 15 MB.
AGENT_PKG := ./cmd/palmux-agent
build-agent:
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS) -s -w" -o $(BIN_DIR)/palmux-agent $(AGENT_PKG)
	@SIZE=$$(stat -c%s $(BIN_DIR)/palmux-agent 2>/dev/null || stat -f%z $(BIN_DIR)/palmux-agent); \
	 MB=$$((SIZE / 1048576)); \
	 echo "  => palmux-agent: $${MB} MB ($${SIZE} bytes)"; \
	 if [ "$$SIZE" -gt 15728640 ]; then \
	   echo "ERROR: palmux-agent exceeds 15 MB limit ($$SIZE bytes)"; exit 1; \
	 fi

# Background-run the production binary, keep its PID in $(SERVE_PID), kill
# the previous process on re-run. Mirrors pattern 6 of port-manager's
# CLAUDE_INTEGRATION.md so `make serve` returns to the shell promptly.
SERVE_PID    := $(TMP_DIR)/palmux$(INSTANCE_SUFFIX).pid
SERVE_LOG    := $(TMP_DIR)/palmux$(INSTANCE_SUFFIX).log

# S009-fix-3: when a non-empty INSTANCE is given, isolate the tmux session
# namespace so this palmux process can't trample the host palmux2's
# `_palmux_*` sessions on a shared tmux server. Default INSTANCE='' keeps
# the canonical `_palmux_` prefix (= every existing install).
#
# We use `_pmx_<instance>_` instead of `_palmux_<instance>_` so an
# unupgraded host palmux running pre-fix-3 code (which only checks
# `HasPrefix(name, "_palmux_")`) doesn't claim the dev instance's
# sessions as its own zombies. A fix-3 host with the strict
# ParseSessionName would already ignore `_palmux_<instance>_*` peers,
# but the `_pmx_*` prefix makes the isolation hold across versions.
SERVE_TMUX_PREFIX := $(if $(INSTANCE),--tmux-prefix=_pmx_$(INSTANCE)_,)

serve: build tmp
	@if [ -f $(SERVE_PID) ]; then \
	  OLD_PID=$$(cat $(SERVE_PID)); \
	  if kill -0 $$OLD_PID 2>/dev/null; then \
	    echo "==> Killing previous palmux2 (PID: $$OLD_PID)..."; \
	    kill $$OLD_PID; \
	    for i in $$(seq 1 50); do kill -0 $$OLD_PID 2>/dev/null || break; sleep 0.1; done; \
	    kill -0 $$OLD_PID 2>/dev/null && kill -9 $$OLD_PID 2>/dev/null || true; \
	  fi; \
	  rm -f $(SERVE_PID); \
	fi
	@PORT=$$($(PALMUX_PORT) alloc --config-dir $(PORT_CONFIG_DIR) --name $(SERVE_NAME)) && \
	  echo "==> Starting palmux2 on port $$PORT (log: $(SERVE_LOG))" && \
	  nohup ./$(BIN_DIR)/palmux \
	    --addr "0.0.0.0:$$PORT" \
	    --config-dir ./$(TMP_DIR) \
	    $(SERVE_TMUX_PREFIX) \
	    > $(SERVE_LOG) 2>&1 & \
	  echo $$! > $(SERVE_PID) && \
	  echo "    PID: $$(cat $(SERVE_PID))"

# Stop the background instance (no restart). Idempotent.
# After killing the process we drop the lease so the next `make serve`
# either reuses the same port (idempotent name → port mapping) or, if the
# user freed it elsewhere, picks a fresh one.
serve-stop:
	@if [ -f $(SERVE_PID) ]; then \
	  OLD_PID=$$(cat $(SERVE_PID)); \
	  if kill -0 $$OLD_PID 2>/dev/null; then \
	    echo "==> Stopping palmux2 (PID: $$OLD_PID)..."; \
	    kill $$OLD_PID; \
	    for i in $$(seq 1 50); do kill -0 $$OLD_PID 2>/dev/null || break; sleep 0.1; done; \
	    kill -0 $$OLD_PID 2>/dev/null && kill -9 $$OLD_PID 2>/dev/null || true; \
	  else \
	    echo "==> Stale pid file; cleaning up."; \
	  fi; \
	  rm -f $(SERVE_PID); \
	else \
	  echo "==> Nothing to stop."; \
	fi
	@if [ -x $(PALMUX_BIN) ] && [ -f $(PORTS_FILE) ]; then \
	  $(PALMUX_PORT) free --config-dir $(PORT_CONFIG_DIR) --name $(SERVE_NAME) 2>/dev/null || true; \
	fi

# Tail the latest server log.
serve-logs:
	@test -f $(SERVE_LOG) || { echo "no log at $(SERVE_LOG)"; exit 1; }
	@tail -f $(SERVE_LOG)

GO_PKGS := $(shell go list ./... 2>/dev/null | grep -v '/frontend/')

test:
	@if [ -z "$(GO_PKGS)" ]; then echo "no Go packages yet"; else go test $(GO_PKGS); fi
	cd frontend && npm test --if-present

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run $(GO_PKGS) || echo "(skipped: golangci-lint not installed)"
	cd frontend && npm run lint

clean:
	rm -rf $(BIN_DIR) frontend/dist
	@mkdir -p frontend/dist
	@touch frontend/dist/.gitkeep

# S025: remove stale palmux2-test fixtures (ghq folder + repos.json).
# Operates only on this repo's tmp/ config-dir. Re-run any time you see
# `palmux2-test--*` entries lingering in the dev drawer.
e2e-cleanup:
	@python3 scripts/cleanup-test-fixtures.py --config-dir ./$(TMP_DIR)

e2e-cleanup-dry:
	@python3 scripts/cleanup-test-fixtures.py --config-dir ./$(TMP_DIR) --dry-run
