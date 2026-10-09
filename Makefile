# guzva ops — the ONLY supported path to production (see docs/runbook.md).
# Never scp a binary by hand: every deploy is a stamped release, healthchecked,
# logged, and rollback-able. Layout follows PLAN §8: releases/<tag> + current.
#
#   make test              fmt + vet + unit tests
#   make build             static binary → dist/guzva (version-stamped)
#   make deploy            test + build + push release + restart + healthcheck
#   make rollback TAG=…    point `current` back, restart, healthcheck
#   make logs | ssh        tail the journal / open a shell
#   make backup            run one snapshot now (the timer does it nightly)
#
# Override the target:  make SSH=root@other deploy

SSH      ?= root@guzva
APP_DIR  := /srv/guzva/bin
REL      := $(APP_DIR)/releases
CUR      := $(APP_DIR)/current
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN      := dist/guzva
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: test build deploy rollback logs ssh backup help

help: ## this list
	@grep -E '^[a-z]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?##"}{printf "  %-10s %s\n",$$1,$$2}'

test: ## gofmt + vet + all unit tests
	gofmt -l cmd internal tools | grep . && exit 1 || true
	go vet ./...
	go test ./...

build: ## static release binary, version-stamped
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/guzva
	@echo "built $(BIN) version=$(VERSION)"

deploy: test build ## ship VERSION: push release, flip symlink, restart, verify
	@echo "deploying $(VERSION) → $(SSH):$(REL)/$(VERSION)/guzva"
	scp $(BIN) $(SSH):/tmp/guzva.new
	ssh $(SSH) 'set -euo pipefail; \
	  mkdir -p $(REL)/$(VERSION); \
	  install -m 0755 -o root -g root /tmp/guzva.new $(REL)/$(VERSION)/guzva; \
	  rm -f /tmp/guzva.new; \
	  ln -sfn $(REL)/$(VERSION) $(CUR); \
	  ls -t $(REL) | tail -n +4 | xargs -r -I{} rm -rf $(REL)/{}; \
	  systemctl restart guzva; sleep 3; systemctl is-active guzva'
	@$(MAKE) --no-print-directory check VERSION=$(VERSION)
	@echo "deployed $(VERSION) — log it in docs/ops-log.md"

rollback: ## rollback to a previous release: make rollback TAG=<tag>
	@test -n "$(TAG)" || (echo "usage: make rollback TAG=<tag>  (ls: releases/)"; exit 2)
	ssh $(SSH) 'set -euo pipefail; \
	  test -x $(REL)/$(TAG)/guzva || (echo "no such release: $(TAG)"; exit 1); \
	  ln -sfn $(REL)/$(TAG) $(CUR); \
	  systemctl restart guzva; sleep 3; systemctl is-active guzva'
	@$(MAKE) --no-print-directory check VERSION=$(TAG)
	@echo "rolled back to $(TAG) — log it in docs/ops-log.md"

check: ## verify what is actually serving (used by deploy/rollback)
	@V=$$(ssh $(SSH) 'curl -sk https://127.0.0.1/healthz'); \
	echo "$$V"; \
	echo "$$V" | grep -q '"ok":true' || (echo "HEALTHCHECK FAILED: healthz"; exit 1); \
	echo "$$V" | grep -q '"version":"$(VERSION)"' || \
	  (echo "HEALTHCHECK FAILED: want version $(VERSION)"; exit 1)
	@ssh $(SSH) 'curl -sk -o /dev/null -w "page /bench/: %{http_code}\n" https://127.0.0.1/bench/ | grep -q 200' \
	  || (echo "HEALTHCHECK FAILED: public page"; exit 1)
	@ssh $(SSH) 'curl -sk -o /dev/null -w "admin: %{http_code}\n" https://127.0.0.1/admin/ | grep -Eq "200|303"' \
	  || (echo "HEALTHCHECK FAILED: admin"; exit 1)
	@echo "healthcheck OK: version $(VERSION), page + admin answer"

logs: ## follow the ingest journal on the VPS
	ssh $(SSH) 'journalctl -u guzva -f | grep ingest'

ssh: ## shell on the VPS
	ssh $(SSH)

backup: ## run one storage snapshot now (timer runs it nightly)
	ssh $(SSH) 'systemctl start guzva-backup.service && journalctl -u guzva-backup -n 5 --no-pager | tail -5'
