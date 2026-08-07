BIN ?= bin
GOFLAGS ?= -trimpath -ldflags "-s -w"
AGENT_TARGETS ?= amd64 arm64 arm

.PHONY: help tidy build hub agent agents run fmt vet clean

help:
	@echo "tidy    resolve and lock dependencies (run this first)"
	@echo "build   build the hub and every agent target into $(BIN)/"
	@echo "hub     build only the hub"
	@echo "agent   build the agent for this machine"
	@echo "agents  cross-build linux agents ($(AGENT_TARGETS))"
	@echo "run     run the hub locally on :8080 with a throwaway database"
	@echo "fmt     gofmt the tree"
	@echo "vet     go vet the tree"

tidy:
	go mod tidy

build: hub agents

hub:
	mkdir -p $(BIN)
	go build $(GOFLAGS) -o $(BIN)/srvmon-hub ./cmd/hub

agent:
	mkdir -p $(BIN)
	go build $(GOFLAGS) -o $(BIN)/srvmon-agent ./cmd/agent

agents:
	mkdir -p $(BIN)
	@for target in $(AGENT_TARGETS); do \
		echo "building linux-$$target"; \
		GOOS=linux GOARCH=$$target CGO_ENABLED=0 go build $(GOFLAGS) \
			-o $(BIN)/srvmon-agent-linux-$$target ./cmd/agent || exit 1; \
	done

run: agents
	go run ./cmd/hub -db ./dev.db -addr :8080 -bin-dir $(BIN)

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -rf $(BIN) dev.db dev.db-wal dev.db-shm
