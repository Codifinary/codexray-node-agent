.PHONY: all
all: lint test

.PHONY: test
test: go-test

.PHONY: lint
lint: go-mod go-vet go-fmt go-imports

.PHONY: go-mod
go-mod:
	go mod tidy

.PHONY: go-vet
go-vet:
	go vet ./...

.PHONY: go-fmt
go-fmt:
	gofmt -w .

.PHONY: go-imports
go-imports:
	go install golang.org/x/tools/cmd/goimports@latest
	goimports -w .

.PHONY: go-test
go-test:
	go test ./...

# ---------------------------------------------------------------------------
# Tests, coverage and the git hooks (see lefthook.yml).
# All of these need CGO + libsystemd headers: sudo apt-get install libsystemd-dev
# ---------------------------------------------------------------------------

COVER_PROFILE ?= cover.out
COVER_MIN     ?= 85.0

.PHONY: test-unit
test-unit:
	CGO_ENABLED=1 go test ./... -count=1

.PHONY: test-race
test-race:
	CGO_ENABLED=1 go test ./... -count=1 -race

.PHONY: cover
cover:
	CGO_ENABLED=1 go test ./... -count=1 -coverprofile=$(COVER_PROFILE)
	go tool cover -func=$(COVER_PROFILE) | tail -1

.PHONY: cover-html
cover-html: cover
	go tool cover -html=$(COVER_PROFILE)

.PHONY: cover-check
cover-check: cover
	@total=$$(go tool cover -func=$(COVER_PROFILE) | awk '/^total:/ {print $$3}' | tr -d '%'); \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { if (t+0 < m+0) { printf "coverage %.1f%% is below the %.1f%% minimum\n", t, m; exit 1 } printf "coverage %.1f%% (minimum %.1f%%)\n", t, m }'

.PHONY: vulncheck
vulncheck:
	@command -v govulncheck >/dev/null 2>&1 || { echo "govulncheck not installed - skipping (go install golang.org/x/vuln/cmd/govulncheck@latest)"; exit 0; }
	govulncheck ./...

.PHONY: hooks
hooks:
	@command -v lefthook >/dev/null 2>&1 || { echo "lefthook not installed: go install github.com/evilmartians/lefthook@latest"; exit 1; }
	lefthook install
