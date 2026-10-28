SERVICE = bborbe/claude-wrapper
INSTALL_BIN ?= $(HOME)/.local/bin/claude-wrapper
CODESIGN_IDENTITY ?= bborbe local codesign

GOIMPORTS_REVISER_VERSION ?= v3.12.6
GOLANGCI_LINT_VERSION ?= v2.14.0

default: precommit

.PHONY: precommit
precommit: tidy format lint test
	@echo "ready to commit"

.PHONY: tidy
tidy:
	go mod tidy
	go mod verify

.PHONY: format
format:
	find . -type f -name '*.go' -not -path './vendor/*' -exec gofmt -w "{}" +
	go run github.com/incu6us/goimports-reviser/v3@$(GOIMPORTS_REVISER_VERSION) \
		-project-name github.com/bborbe/claude-wrapper -format ./...

.PHONY: lint
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

.PHONY: test
test:
	go test -mod=mod -count=1 ./...

.PHONY: build
build:
	go build -o "$(INSTALL_BIN).new" .

# The binary is signed before it is moved into place, so a rebuild never leaves
# an ad-hoc-signed file that TCC treats as a different app on the next start.
.PHONY: install
install:
	@security find-identity -p codesigning | grep -qF '"$(CODESIGN_IDENTITY)"' \
		|| { echo "codesign identity '$(CODESIGN_IDENTITY)' not in keychain" >&2; exit 1; }
	mkdir -p "$(dir $(INSTALL_BIN))"
	go build -o "$(INSTALL_BIN).new" .
	codesign -f -s "$(CODESIGN_IDENTITY)" -i de.bborbe.claude-wrapper "$(INSTALL_BIN).new"
	codesign --verify --strict "$(INSTALL_BIN).new"
	mv -f "$(INSTALL_BIN).new" "$(INSTALL_BIN)"

.PHONY: run
run:
	go run -mod=mod . -listen "localhost:$(CLAUDE_WRAPPER_PORT)" -claude-bin claude
