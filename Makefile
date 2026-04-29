.PHONY: dev dev-api dev-frontend ports tmp serve serve-stop serve-logs build build-linux build-arm test lint clean

TMP_DIR := tmp
ENV_FILE := $(TMP_DIR)/portman.env
BIN_DIR := bin
GO_PKG := ./cmd/palmux

# Optional instance suffix so a second palmux2 (e.g. in a `dev` worktree)
# can run side-by-side with the host instance without sharing portman
# names — and therefore without sharing ports. Default: blank → host
# instance ("palmux2", "palmux2-api", "palmux2-frontend"). Override with
# `make serve INSTANCE=dev` etc.
INSTANCE ?=
INSTANCE_SUFFIX := $(if $(INSTANCE),-$(INSTANCE),)
SERVE_NAME    := palmux2$(INSTANCE_SUFFIX)
API_NAME      := palmux2$(INSTANCE_SUFFIX)-api
FRONTEND_NAME := palmux2$(INSTANCE_SUFFIX)-frontend

# Re-lease ports each invocation. portman returns the same port for the same
# project/branch/name combo, so this is stable across runs.
ports: tmp
	@portman env --expose --name $(API_NAME) --name $(FRONTEND_NAME) --output $(ENV_FILE)
	@cat $(ENV_FILE)

tmp:
	@mkdir -p $(TMP_DIR)

dev: ports
	@$(MAKE) -j2 dev-api dev-frontend

# portman env writes ports as PALMUX2_<NAME>_PORT, where <NAME> is the
# uppercased portman name with hyphens turned into underscores. We compute
# the variable names here so they track INSTANCE.
API_PORT_VAR      := PALMUX2$(shell echo $(INSTANCE_SUFFIX) | tr 'a-z-' 'A-Z_')_API_PORT
FRONTEND_PORT_VAR := PALMUX2$(shell echo $(INSTANCE_SUFFIX) | tr 'a-z-' 'A-Z_')_FRONTEND_PORT

dev-api: ports
	@. $(ENV_FILE) && \
		go run $(GO_PKG) \
			--addr "0.0.0.0:$${$(API_PORT_VAR)}" \
			--config-dir ./$(TMP_DIR)

dev-frontend: ports
	@. $(ENV_FILE) && cd frontend && \
		PALMUX2_API_PORT=$${$(API_PORT_VAR)} \
		npm run dev -- --port $${$(FRONTEND_PORT_VAR)} --host 0.0.0.0 --strictPort

# Production: embed-built frontend, single binary
build: build-frontend
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/palmux $(GO_PKG)

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
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $(BIN_DIR)/palmux-linux-amd64 $(GO_PKG)

build-arm: build-frontend
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o $(BIN_DIR)/palmux-linux-arm64 $(GO_PKG)

# Background-run the production binary, keep its PID in $(SERVE_PID), kill
# the previous process on re-run. Mirrors pattern 6 of port-manager's
# CLAUDE_INTEGRATION.md so `make serve` returns to the shell promptly.
SERVE_PID    := $(TMP_DIR)/palmux$(INSTANCE_SUFFIX).pid
SERVE_LOG    := $(TMP_DIR)/palmux$(INSTANCE_SUFFIX).log
SERVE_ENV    := $(TMP_DIR)/palmux$(INSTANCE_SUFFIX).portman.env
SERVE_PORT_VAR := PALMUX2$(shell echo $(INSTANCE_SUFFIX) | tr 'a-z-' 'A-Z_')_PORT

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
	@portman env --name $(SERVE_NAME) --expose --output $(SERVE_ENV)
	@portman sync >/dev/null
	@. $(SERVE_ENV) && \
	  PORT=$${$(SERVE_PORT_VAR)} && \
	  echo "==> Starting palmux2 on port $$PORT (log: $(SERVE_LOG))" && \
	  nohup ./$(BIN_DIR)/palmux \
	    --addr "0.0.0.0:$$PORT" \
	    --config-dir ./$(TMP_DIR) \
	    > $(SERVE_LOG) 2>&1 & \
	  echo $$! > $(SERVE_PID) && \
	  echo "    PID: $$(cat $(SERVE_PID))"

# Stop the background instance (no restart). Idempotent.
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
