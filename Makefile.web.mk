# guzva — design system build (PLAN §9, D6)
#
# Tailwind CLI is vendored as a portable *download* (not a repo blob): the
# 111 MB binary would bloat git and would be pinned forever. Anything that
# runs `make css` gets the same byte-for-byte binary first.
#
# Override the binary location without touching the tree:
#   make TAILWIND=/usr/local/bin/tailwindcss css

TAILWIND_VERSION ?= v4.3.3
TAILWIND         ?= tools/bin/tailwindcss
TAILWIND_URL     := https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-linux-x64

.PHONY: css
css: css-out ## alias for css-out

$(TAILWIND):
	@mkdir -p tools/bin
	curl -fsSL -o $@ $(TAILWIND_URL)
	@chmod +x $@

css-out: $(TAILWIND) ## compile the tokens + templates into internal/webui/app.css
	$(TAILWIND) --input web/input.css --output internal/webui/app.css --minify

.PHONY: css-check
css-check: ## fail CI if web/app.css is stale wrt the tokens/templates
	@cp internal/webui/app.css /tmp/guzva-app.css.bak 2>/dev/null || :; \
	$(MAKE) --no-print-directory css-out; \
	if ! cmp -s internal/webui/app.css /tmp/guzva-app.css.bak; then \
	  echo "web/app.css is stale — run 'make css' and commit internal/webui/app.css"; \
	  rm -f /tmp/guzva-app.css.bak; \
	  exit 1; \
	fi; \
	rm -f /tmp/guzva-app.css.bak
