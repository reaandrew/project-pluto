# ai-website-agency — top-level developer make targets
#
# `make ci-local` mirrors the gating jobs in .github/workflows/deploy.yml so
# fmt / vet / lint / test failures get caught locally — saves a ~12-min CI
# round-trip on a one-line mistake. See .ralph/specs/12-ci-efficiency.md.

.PHONY: ci-local lambdas-quality frontend-quality worker-quality terraform-quality secret-scan help

help:
	@echo "Targets:"
	@echo "  ci-local           Run every gating job locally (full CI mirror)"
	@echo "  lambdas-quality    gofmt + go vet + golangci-lint + go test -race"
	@echo "  frontend-quality   typecheck + lint + format:check + test:coverage"
	@echo "  worker-quality     typecheck + lint + test (Cloudflare worker)"
	@echo "  terraform-quality  terraform fmt -check + validate (every stack)"
	@echo "  secret-scan        gitleaks (locally — runs trufflehog in CI)"

ci-local: lambdas-quality frontend-quality worker-quality terraform-quality secret-scan
	@echo "✓ ci-local passed"

lambdas-quality:
	@if [ ! -d lambdas ]; then echo "(no lambdas/ — skipping)"; exit 0; fi
	cd lambdas && gofmt -l . | (! grep .) && go vet ./... && golangci-lint run --timeout=5m && go test -race ./...

frontend-quality:
	@if [ ! -d frontend ]; then echo "(no frontend/ — skipping)"; exit 0; fi
	cd frontend && npm ci && npm run typecheck && npm run lint && npm run format:check && npm run test:coverage

worker-quality:
	@if [ ! -d worker ]; then echo "(no worker/ — skipping)"; exit 0; fi
	cd worker && npm ci && npm run typecheck && npm run lint && npm run test

terraform-quality:
	@for d in terraform aws-setup cloudflare; do \
		if [ -d "$$d" ]; then \
			echo "→ $$d"; \
			(cd "$$d" && terraform fmt -recursive -check && terraform validate) || exit $$?; \
		fi; \
	done

secret-scan:
	@if command -v gitleaks >/dev/null; then \
		gitleaks detect --no-banner; \
	else \
		echo "(gitleaks not installed — install via 'brew install gitleaks' or skip; CI runs trufflehog)"; \
	fi
