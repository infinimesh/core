VERSION ?= "latest"

build-all:
	sh hack/build_images.sh

build-console:
	export INFINIMESH_VERSION_TAG=$(git describe --tags --abbrev=0)
	docker build . -f "Dockerfiles/console/Dockerfile" -t "ghcr.io/infinimesh/infinimesh/console:${VERSION}"

build-repo:
	docker build . -f "Dockerfiles/repo/Dockerfile" -t "ghcr.io/infinimesh/infinimesh/repo:${VERSION}"

build-mqtt:
	docker build . -f "Dockerfiles/mqtt-bridge/Dockerfile" -t "ghcr.io/infinimesh/infinimesh/mqtt-bridge:${VERSION}"

build-shadow:
	docker build . -f "Dockerfiles/shadow/Dockerfile" -t "ghcr.io/infinimesh/infinimesh/shadow:${VERSION}"

mocks:
	docker run -v "$PWD":/src -w /src vektra/mockery --all

GOBIN ?= $$(go env GOPATH)/bin

.PHONY: install-go-test-coverage
install-go-test-coverage:
	go install github.com/vladopajic/go-test-coverage/v2@latest

.PHONY: check-coverage
check-coverage: install-go-test-coverage
	go test ./... -coverprofile=./cover.out -covermode=atomic -coverpkg=./...
	${GOBIN}/go-test-coverage --config=./.testcoverage.yml

check-coverage-html: check-coverage
	rm cover.html 2> /dev/null
	go tool cover -html=cover.out -o=cover.html
	
.PHONY: build-all build-console mocks

.PHONY: fmt fmt-check race test-fuzz code-ql code-ql-summary code-ql-gate commit-check

fmt: ## Format Go files that gofmt would change
	@unformatted=$$(find . -name '*.go' -not -path './vendor/*' -not -path '*/node_modules/*' -not -path './.tmp/*' -print | xargs gofmt -l); \
	if [ -n "$$unformatted" ]; then \
		echo "Formatting Go files:"; \
		printf '%s\n' "$$unformatted"; \
		printf '%s\n' "$$unformatted" | while IFS= read -r f; do [ -n "$$f" ] && gofmt -w "$$f"; done; \
		echo "fmt passed."; \
	else \
		echo "All Go files are formatted correctly."; \
		echo "fmt passed."; \
	fi

fmt-check: ## Fail if any Go file is not gofmt-clean
	@unformatted=$$(find . -name '*.go' -not -path './vendor/*' -not -path '*/node_modules/*' -not -path './.tmp/*' -print | xargs gofmt -l); \
	if [ -n "$$unformatted" ]; then \
		echo "The following files are not gofmt-formatted:"; \
		printf '%s\n' "$$unformatted"; \
		echo "Run 'make fmt' to fix them."; \
		exit 1; \
	fi

race: ## Run tests with the race detector
	@echo "==> go test -race ./..."
	@go test -race ./...
	@echo "race passed."

test-fuzz: ## Run Go fuzz tests
	bash scripts/test_fuzz.sh

code-ql: ## Run local CodeQL and write SARIF under .tmp/codeql/
	bash scripts/codeql_local.sh

code-ql-summary: code-ql ## Run CodeQL and print a readable summary from SARIF
	@jq -r '.runs[]?.results[]? | select((.locations[0].physicalLocation.artifactLocation.uri // "") | test("(^|/)vendor/") | not) | "\(.level // "warning")\t\(.ruleId // "no-rule")\t\(.locations[0].physicalLocation.artifactLocation.uri // "unknown"):\(.locations[0].physicalLocation.region.startLine // 0)\t\(.message.text // "no-message")"' .tmp/codeql/*.sarif | sort || true

code-ql-gate: code-ql ## Fail if CodeQL reports any error findings
	@errors=$$(jq '[.runs[]?.results[]? | select((.level // "warning") == "error") | select((.locations[0].physicalLocation.artifactLocation.uri // "") | test("(^|/)vendor/") | not)] | length' .tmp/codeql/*.sarif | awk '{s+=$$1} END{print s+0}'); \
	if [ "$$errors" -gt 0 ]; then \
		echo "CodeQL gate failed: $$errors error finding(s) found."; \
		exit 1; \
	fi; \
	echo "CodeQL gate passed: no error findings found."

commit-check: fmt race test-fuzz code-ql-gate ## Format, race, fuzz, and CodeQL
	@echo "commit-check passed."