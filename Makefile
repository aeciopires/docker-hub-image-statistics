# docker-hub-image-statistics — development tasks.
# Compatible with GNU make 3.81+ (macOS default) and 4.x (Ubuntu).
# Run "make help" to list the targets.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Variables (override on the command line: make docker-build IMAGE=me/dhis)
# ---------------------------------------------------------------------------
APP          := docker-hub-image-statistics
MODULE       := github.com/aeciopires/docker-hub-image-statistics
VERSION      := $(shell tr -d ' \n\r' < VERSION)
COMMIT       := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE   := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE        ?= aeciopires/$(APP)
PLATFORMS    ?= linux/amd64,linux/arm64
BIN_DIR      := bin
DIST_DIR     := dist
COVERAGE_MIN ?= 90
COVER_FILE   := coverage.out
CHART_DIR    := charts/$(APP)
COMPOSE_DIR  := deploy/compose
COMPOSE      := docker compose -f $(COMPOSE_DIR)/compose.yaml
COMPOSE_FULL := $(COMPOSE) -f $(COMPOSE_DIR)/compose.observability.yaml
MISE         := mise exec --
GO           := $(MISE) go
UNAME_S      := $(shell uname -s)
COLIMA_CPU   ?= 4
COLIMA_MEMORY ?= 8
COLIMA_DISK  ?= 60
CONFIG       ?= config.yaml
KIND_CLUSTER ?= dhis
KIND_CONFIG  ?= deploy/kind/kind-config.yaml
KUBE_CONTEXT ?= kind-$(KIND_CLUSTER)
K8S_NAMESPACE ?= dhis
HELM_RELEASE ?= dhis
HELM_VALUES  ?= deploy/kind/values.yaml
HELM         := $(MISE) helm --kube-context $(KUBE_CONTEXT) -n $(K8S_NAMESPACE)
KUBECTL      := $(MISE) kubectl --context $(KUBE_CONTEXT) -n $(K8S_NAMESPACE)
# The "[b]" keeps pkill from matching the shell that runs it.
APP_PATTERN  := [b]in/$(APP) serve

LDFLAGS := -s -w -buildid= \
  -X $(MODULE)/internal/version.Version=$(VERSION) \
  -X $(MODULE)/internal/version.Commit=$(COMMIT) \
  -X $(MODULE)/internal/version.Date=$(BUILD_DATE)

export TAG := $(VERSION)
export IMAGE
export COMMIT
export BUILD_DATE

.PHONY: help
help: ## Show this help
	@printf "\nUsage: make \033[36m<target>\033[0m  (version $(VERSION))\n"
	@awk 'BEGIN {FS = ":.*##"} /^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5)} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

##@ Dependencies
.PHONY: check-deps install-deps
check-deps: ## Check that every development dependency is installed
	@scripts/check-deps.sh

install-deps: ## Install development dependencies with mise (Docker is installed separately)
	@scripts/install-deps.sh

##@ Container runtime on macOS (Colima)
.PHONY: colima-start colima-stop colima-status
colima-start: ## macOS: start Colima (COLIMA_CPU=4 COLIMA_MEMORY=8 GiB COLIMA_DISK=60 GiB) and use its Docker context
	@[ "$(UNAME_S)" = "Darwin" ] || { echo "Colima is only used on macOS (Linux uses Docker Engine)"; exit 1; }
	@if colima status >/dev/null 2>&1; then echo "Colima is already running"; \
	else colima start --cpu $(COLIMA_CPU) --memory $(COLIMA_MEMORY) --disk $(COLIMA_DISK); fi
	@docker context use colima >/dev/null && docker version --format 'Docker Engine {{.Server.Version}} via Colima'

colima-stop: ## macOS: stop Colima (containers stop, volumes are kept)
	@[ "$(UNAME_S)" = "Darwin" ] || { echo "Colima is only used on macOS"; exit 1; }
	@colima stop

colima-status: ## macOS: show the Colima VM status and resources
	@[ "$(UNAME_S)" = "Darwin" ] || { echo "Colima is only used on macOS"; exit 1; }
	@colima status && colima list

##@ Version
.PHONY: version version-sync version-check
version: ## Print the version from the VERSION file
	@echo $(VERSION)

version-sync: ## Propagate VERSION to the Helm chart (version/appVersion) and its README
	@scripts/version-sync.sh

version-check: ## Fail if the chart or CHANGELOG are not in sync with VERSION
	@scripts/version-sync.sh --check

##@ Development
.PHONY: fmt fmt-check vet lint vuln run reload tidy
fmt: ## Format the Go code
	@$(MISE) golangci-lint fmt ./...

fmt-check: ## Fail if the Go code is not formatted
	@out="$$($(MISE) gofmt -l cmd internal)"; if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

vet: ## Run go vet
	@$(GO) vet ./...

lint: ## Run golangci-lint
	@$(MISE) golangci-lint run ./...

vuln: ## Scan Go dependencies for known vulnerabilities (govulncheck)
	@$(MISE) govulncheck ./...

tidy: ## Tidy go.mod/go.sum
	@$(GO) mod tidy

run: build ## Run the app locally (CONFIG=config.yaml when present); reload it with "make reload"
	@if [ -f "$(CONFIG)" ]; then exec $(BIN_DIR)/$(APP) serve --config "$(CONFIG)"; else exec $(BIN_DIR)/$(APP) serve; fi

reload: ## Reload the configuration of the app started by "make run" (SIGHUP, no restart)
	@pids="$$(pgrep -u "$$(id -u)" -f '$(APP_PATTERN)' || true)"; \
	if [ -z "$$pids" ]; then echo "the app is not running (start it with: make run)"; exit 1; fi; \
	kill -HUP $$pids; \
	echo "SIGHUP sent to pid(s) $$pids: config reloaded (an invalid file is rejected and the current one kept; see the logs)"

##@ Tests
.PHONY: test test-race coverage coverage-html
test: ## Run the unit tests
	@$(GO) test ./...

test-race: ## Run the unit tests with the race detector
	@$(GO) test -race ./...

coverage: ## Run the tests and enforce the minimum coverage (COVERAGE_MIN, default 90%)
	@$(GO) test -race -covermode=atomic -coverprofile=$(COVER_FILE) ./internal/...
	@total="$$($(GO) tool cover -func=$(COVER_FILE) | awk '/^total:/ {sub("%","",$$3); print $$3}')"; \
	 echo "total coverage: $${total}% (minimum $(COVERAGE_MIN)%)"; \
	 awk -v t="$$total" -v m="$(COVERAGE_MIN)" 'BEGIN { exit (t + 0 < m + 0) }' || { echo "coverage below $(COVERAGE_MIN)%"; exit 1; }

coverage-html: coverage ## Open the HTML coverage report
	@$(GO) tool cover -html=$(COVER_FILE)

##@ Build
.PHONY: build build-all clean
build: ## Build the binary for the current platform into bin/
	@mkdir -p $(BIN_DIR)
	@CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(APP) ./cmd/$(APP)
	@echo "built $(BIN_DIR)/$(APP) ($(VERSION))"

build-all: ## Cross-compile release binaries (linux/darwin, amd64/arm64) into dist/ with checksums
	@mkdir -p $(DIST_DIR)
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
	  os="$${target%/*}"; arch="$${target#*/}"; \
	  out="$(DIST_DIR)/$(APP)_$(VERSION)_$${os}_$${arch}"; \
	  echo "building $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -buildvcs=false -ldflags "$(LDFLAGS)" -o "$$out" ./cmd/$(APP); \
	done
	@cd $(DIST_DIR) && (command -v sha256sum >/dev/null && sha256sum $(APP)_* || shasum -a 256 $(APP)_*) > SHA256SUMS
	@echo "checksums written to $(DIST_DIR)/SHA256SUMS"

clean: ## Remove build artifacts
	@rm -rf $(BIN_DIR) $(DIST_DIR) $(COVER_FILE) data .tmp

##@ Docker image
.PHONY: docker-lint docker-build docker-push docker-scan docker-run
docker-lint: ## Lint the Dockerfile with hadolint
	@$(MISE) hadolint Dockerfile

docker-build: ## Build the image for the local platform (IMAGE:VERSION and IMAGE:latest)
	@docker build \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-push: ## Build and push a multi-arch image (PLATFORMS) tagged VERSION and latest
	@docker buildx build --platform $(PLATFORMS) \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
	  --provenance=true --sbom=true \
	  -t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .

docker-scan: ## Scan the image for vulnerabilities, secrets and misconfigurations with trivy
	@$(MISE) trivy image --config trivy.yaml --ignorefile .trivyignore.yaml $(IMAGE):$(VERSION)

docker-run: ## Run the image locally on port 8080
	@docker run --rm -it -p 8080:8080 --read-only --cap-drop ALL --security-opt no-new-privileges \
	  -v $(APP)-data:/data $(IMAGE):$(VERSION)

##@ Application (docker compose)
.PHONY: up down restart compose-reload logs ps
up: ## Start the application only (http://localhost:8080)
	@$(COMPOSE) up -d --build
	@echo "UI: http://localhost:8080"

down: ## Stop the application
	@$(COMPOSE) down

restart: ## Restart the application container
	@$(COMPOSE) restart app

compose-reload: ## Reload deploy/compose/config/app/config.yaml in the running container (SIGHUP; app or stack)
	@$(COMPOSE) kill -s SIGHUP app
	@echo "SIGHUP sent to the app container; check with: make logs"

logs: ## Follow the application logs
	@$(COMPOSE) logs -f app

ps: ## Show the application containers
	@$(COMPOSE) ps

##@ Observability stack (app + OTel Collector, Prometheus, Loki, Tempo, Jaeger, Grafana)
.PHONY: stack-up stack-down stack-clean stack-logs stack-ps
stack-up: ## Start the app with the full observability stack
	@$(COMPOSE_FULL) up -d --build
	@echo "App:        http://localhost:8080"
	@echo "Grafana:    http://localhost:3000  (admin/admin unless GRAFANA_ADMIN_PASSWORD is set)"
	@echo "Prometheus: http://localhost:9090"
	@echo "Jaeger:     http://localhost:16686"

stack-down: ## Stop the stack (keeps data volumes)
	@$(COMPOSE_FULL) down

stack-clean: ## Stop the stack and delete its data volumes
	@$(COMPOSE_FULL) down -v

stack-logs: ## Follow the logs of every stack service
	@$(COMPOSE_FULL) logs -f

stack-ps: ## Show the stack containers
	@$(COMPOSE_FULL) ps

##@ Local Kubernetes (kind + Helm 4)
.PHONY: kind-create kind-delete kind-load kind-up helm-install helm-reload helm-test helm-uninstall helm-status kind-port-forward kind-logs
kind-create: ## Create the kind cluster and a namespace enforcing the restricted Pod Security Standard
	@if $(MISE) kind get clusters 2>/dev/null | grep -qx '$(KIND_CLUSTER)'; then \
	  echo "kind cluster '$(KIND_CLUSTER)' already exists"; \
	else $(MISE) kind create cluster --name $(KIND_CLUSTER) --config $(KIND_CONFIG) --wait 180s; fi
	@$(MISE) kubectl --context $(KUBE_CONTEXT) create namespace $(K8S_NAMESPACE) --dry-run=client -o yaml \
	  | $(MISE) kubectl --context $(KUBE_CONTEXT) apply -f - >/dev/null
	@$(MISE) kubectl --context $(KUBE_CONTEXT) label namespace $(K8S_NAMESPACE) --overwrite \
	  pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/warn=restricted >/dev/null
	@echo "cluster '$(KIND_CLUSTER)' ready (context $(KUBE_CONTEXT), namespace $(K8S_NAMESPACE))"

kind-delete: ## Delete the kind cluster
	@$(MISE) kind delete cluster --name $(KIND_CLUSTER)

kind-load: docker-build ## Build the image and load it into the kind cluster (no registry needed)
	@$(MISE) kind load docker-image $(IMAGE):$(VERSION) --name $(KIND_CLUSTER)

kind-up: kind-create kind-load helm-install ## Create the cluster, load the image and install the chart

helm-install: ## Install/upgrade the chart on kind using HELM_VALUES (deploy/kind/values.yaml)
	@$(HELM) upgrade --install $(HELM_RELEASE) $(CHART_DIR) -f $(HELM_VALUES) \
	  --set image.registry=docker.io --set image.repository=$(IMAGE) --set image.tag=$(VERSION) --wait --timeout 5m
	@$(KUBECTL) get pods -l app.kubernetes.io/instance=$(HELM_RELEASE)

helm-reload: ## Apply the edited HELM_VALUES and roll the pod (helm upgrade + restart annotation)
	@$(HELM) upgrade $(HELM_RELEASE) $(CHART_DIR) -f $(HELM_VALUES) \
	  --set image.registry=docker.io --set image.repository=$(IMAGE) --set image.tag=$(VERSION) \
	  --set-string podAnnotations.reloaded-at="$$(date +%s)" --wait --timeout 5m
	@$(KUBECTL) get pods -l app.kubernetes.io/instance=$(HELM_RELEASE)

helm-uninstall: ## Uninstall the release (the data PVC is kept on purpose)
	@$(HELM) uninstall $(HELM_RELEASE) --wait
	@echo "PVC kept (helm.sh/resource-policy: keep). Delete it with: kubectl --context $(KUBE_CONTEXT) -n $(K8S_NAMESPACE) delete pvc $(HELM_RELEASE)-$(APP)-data"

helm-test: ## Run "helm test" on kind (the image's healthcheck command against /readyz)
	@$(HELM) test $(HELM_RELEASE) --logs

helm-status: ## Show the release status and its pods, service and PVC
	@$(HELM) status $(HELM_RELEASE)
	@$(KUBECTL) get pods,svc,pvc -l app.kubernetes.io/instance=$(HELM_RELEASE)

kind-port-forward: ## Forward http://localhost:8080 to the service in kind
	@$(KUBECTL) port-forward svc/$(HELM_RELEASE)-$(APP) 8080:8080

kind-logs: ## Follow the application logs in kind
	@$(KUBECTL) logs -f deploy/$(HELM_RELEASE)-$(APP)

##@ Helm chart (Helm 4)
.PHONY: helm-lint helm-template helm-validate helm-docs helm-package
helm-lint: ## Lint the chart (default and all-features values)
	@$(MISE) helm lint $(CHART_DIR) --strict
	@$(MISE) helm lint $(CHART_DIR) --strict -f $(CHART_DIR)/ci/all-features-values.yaml

helm-template: ## Render the chart with every feature enabled
	@$(MISE) helm template $(APP) $(CHART_DIR) -f $(CHART_DIR)/ci/all-features-values.yaml

helm-validate: helm-lint ## Validate rendered manifests (kubeconform) and scan them (trivy)
	@$(MISE) helm template $(APP) $(CHART_DIR) -f $(CHART_DIR)/ci/all-features-values.yaml \
	  | $(MISE) kubeconform -strict -summary -ignore-missing-schemas
	@$(MISE) trivy config --quiet --exit-code 1 --severity MEDIUM,HIGH,CRITICAL \
	  --ignorefile .trivyignore.yaml --helm-values $(CHART_DIR)/ci/all-features-values.yaml $(CHART_DIR)

helm-docs: ## Regenerate the chart README with helm-docs
	@cd $(CHART_DIR) && $(MISE) helm-docs --template-files README.md.gotmpl

helm-package: version-check ## Package the chart into dist/
	@mkdir -p $(DIST_DIR)
	@$(MISE) helm package $(CHART_DIR) -d $(DIST_DIR)

##@ Documentation and dashboards
.PHONY: docs-validate dashboards
docs-validate: ## Validate every Mermaid diagram with the mermaid-cli Docker image
	@scripts/validate-mermaid.sh

dashboards: ## Regenerate the Grafana dashboards (JSON) from scripts/gen-dashboards.py
	@python3 scripts/gen-dashboards.py

##@ Quality gates
.PHONY: scan-fs check
scan-fs: ## Scan the repository (dependencies, secrets, IaC) with trivy
	@$(MISE) trivy fs --quiet --exit-code 1 --severity MEDIUM,HIGH,CRITICAL --scanners vuln,secret,misconfig \
	  --ignorefile .trivyignore.yaml --skip-dirs $(DIST_DIR) .

check: fmt-check vet lint coverage vuln version-check docker-lint helm-validate ## Run every check (CI)
	@echo "all checks passed"
