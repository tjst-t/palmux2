.PHONY: dev dev-api dev-frontend ports tmp serve build build-linux build-arm test lint clean

TMP_DIR := tmp
ENV_FILE := $(TMP_DIR)/portman.env
BIN_DIR := bin
GO_PKG := ./cmd/palmux

# Re-lease ports each invocation. portman returns the same port for the same
# project/branch/name combo, so this is stable across runs.
ports: tmp
	@portman env --expose --name palmux2-api --name palmux2-frontend --output $(ENV_FILE)
	@cat $(ENV_FILE)

tmp:
	@mkdir -p $(TMP_DIR)

dev: ports
	@$(MAKE) -j2 dev-api dev-frontend

dev-api: ports
	@. $(ENV_FILE) && \
		go run $(GO_PKG) \
			--addr "0.0.0.0:$$PALMUX2_API_PORT" \
			--config-dir ./$(TMP_DIR)

dev-frontend: ports
	@. $(ENV_FILE) && cd frontend && \
		PALMUX2_API_PORT=$$PALMUX2_API_PORT \
		npm run dev -- --port $$PALMUX2_FRONTEND_PORT --host 0.0.0.0 --strictPort

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

# Run the production binary via portman (single port)
serve: build tmp
	portman exec --name palmux2 --expose -- ./$(BIN_DIR)/palmux \
		--addr "0.0.0.0:{}" \
		--config-dir ./$(TMP_DIR)

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
