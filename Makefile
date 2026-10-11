# guzva ops — the ONLY supported path to production (see docs/runbook.md).
# Never scp a binary by hand: every deploy is a stamped release, healthchecked,
# logged, and rollback-able. Layout follows PLAN §8: releases/<tag> + current.
#
#   make test              fmt + vet + unit tests
#   make build             static binary → dist/guzva (version-stamped)
#   make tag V=v0.4.0      annotated release tag (VERSION comes from tags)
#   make deploy            test + build + push release + restart + healthcheck
#   make rollback TAG=…    point `current` back, restart, healthcheck
#   make drill             restore drill on the VPS (log it in ops-log.md)
#   make logs | ssh        tail the journal / open a shell
#   make backup            run one snapshot now (the timer does it nightly)
#
# Release flow: tag → deploy → log. VERSION is `git describe --tags`, so an
# untagged deploy ships under a bare SHA and rollback has no name to hold.
#
# The UI stylesheet is a third release artifact: `make css` runs the Tailwind
# CLI over the templates when `make deploy` runs. `make test` fails CI if
# web/app.css is stale (no hand-edits to it go in with the image).
#
# Override the target:  make SSH=root@other deploy

SSH      ?= root@guzva
APP_DIR  := /srv/guzva/bin
REL      := $(APP_DIR)/releases
CUR      := $(APP_DIR)/current
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN      := dist/guzva
LDFLAGS  := -s -w -X main.version=$(VERSION)

include Makefile.web.mk

.PHONY: test build tag deploy rollback drill logs ssh backup help

help: ## this list
	@grep -E '^[a-z]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?##"}{printf "  %-10s %s\n",$$1,$$2}'

test: css-check ## gofmt + vet + unit tests (fails on stale UI or build drift)
	gofmt -l cmd internal tools | grep . && exit 1 || true
	go vet ./...
	go test ./...

build: css ## static release binary, version-stamped + design system
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

tag: test ## create an annotated release tag: make tag V=v0.4.0 (then deploy)
	@test -n "$(V)" || (echo "usage: make tag V=v0.4.0   (VERSION = git describe)"; exit 2)
	@git diff-index --quiet HEAD || (echo "working tree is dirty — commit first (a tag must name a real state)"; exit 2)
	git tag -a "$(V)" -m "release $(V)"
	@echo "tagged $(V); make deploy ships it as releases/$(V)"

drill: ## restore drill on the VPS (W5: prove the nightly backup restorable)
	scp -q tools/restore-drill.sh $(SSH):/tmp/guzva-restore-drill.sh
	ssh $(SSH) 'bash /tmp/guzva-restore-drill.sh; rc=$$?; rm -f /tmp/guzva-restore-drill.sh; exit $$rc'
	@echo "drill finished — record it in docs/ops-log.md (mandatory before the pilot, then quarterly)"

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
