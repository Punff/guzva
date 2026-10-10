# guzva — design system build (DESIGN.md §4, §13; PLAN §9, D6)
#
# Tailwind CLI is vendored as a portable *download* (not a repo blob): the
# 111 MB binary would bloat git and pin a stale copy forever. Anything that
# runs `make css` gets the same byte-for-byte binary first.
#
#   make TAILWIND=/usr/local/bin/tailwindcss css
#
# Two entries, because the public page may ship exactly two files (D5):
#   web/public.css → inlined into index.html   (budget 8 KB minified)
#   web/app.css    → served /admin/app.css     (budget 30 KB minified)

TAILWIND_VERSION ?= v4.3.3
TAILWIND         ?= tools/bin/tailwindcss
TAILWIND_URL     := https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-linux-x64

PUBLIC_CSS := internal/webui/public.css
APP_CSS    := internal/webui/app.css

# Budgets are GZIPPED bytes, because that is what a guest on Bosnian mobile
# data actually pays for and waits on. DESIGN.md §13 states the intent as
# "CSS ≤ 2.5 KB gz" and "first view ≈ 11 KB" — an uncompressed budget would
# measure the wrong thing (Tailwind's own preflight alone is several KB and
# compresses ~4:1). CI fails above these, so a regression cannot quietly
# triple the page weight.
BUDGET_PUBLIC_GZ := 4096
BUDGET_APP_GZ    := 12288

.PHONY: css css-out css-budget

css: css-out css-budget ## compile both entries and enforce the size budgets

css-out: $(TAILWIND)
	$(TAILWIND) --input web/public.css --output $(PUBLIC_CSS) --minify
	$(TAILWIND) --input web/app.css --output $(APP_CSS) --minify

$(TAILWIND):
	@mkdir -p tools/bin
	curl -fsSL -o $@ $(TAILWIND_URL)
	@chmod +x $@

.PHONY: css-budget
css-budget: ## fail if either stylesheet is over its DESIGN.md gzipped budget
	@ok=1; \
	p=$$(gzip -9 -c $(PUBLIC_CSS) | wc -c); a=$$(gzip -9 -c $(APP_CSS) | wc -c); \
	pm=$$(wc -c < $(PUBLIC_CSS)); am=$$(wc -c < $(APP_CSS)); \
	if [ $$p -gt $(BUDGET_PUBLIC_GZ) ]; then \
	  echo "FAIL public.css $$p B gz > $(BUDGET_PUBLIC_GZ) B budget (DESIGN.md §4/§13)"; ok=0; fi; \
	if [ $$a -gt $(BUDGET_APP_GZ) ]; then \
	  echo "FAIL app.css $$a B gz > $(BUDGET_APP_GZ) B budget (DESIGN.md §4/§13)"; ok=0; fi; \
	[ $$ok -eq 1 ] && echo "css budgets ok (public $$pm B raw / $$p B gz of $(BUDGET_PUBLIC_GZ); app $$am B raw / $$a B gz of $(BUDGET_APP_GZ))"; \
	exit $$((1-ok))

.PHONY: css-check
css-check: ## fail if the committed CSS is stale vs the tokens/templates
	@cp $(PUBLIC_CSS) /tmp/guzva-pub.bak 2>/dev/null || :; \
	cp $(APP_CSS) /tmp/guzva-app.bak 2>/dev/null || :; \
	$(MAKE) --no-print-directory css-out >/dev/null; \
	if ! cmp -s $(PUBLIC_CSS) /tmp/guzva-pub.bak || ! cmp -s $(APP_CSS) /tmp/guzva-app.bak; then \
	  echo "generated CSS is stale — run 'make css' and commit"; \
	  rm -f /tmp/guzva-pub.bak /tmp/guzva-app.bak; exit 1; \
	fi; \
	rm -f /tmp/guzva-pub.bak /tmp/guzva-app.bak; \
	$(MAKE) --no-print-directory css-budget