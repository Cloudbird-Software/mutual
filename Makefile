.PHONY: setup fmt lint arch test build check go-fmt go-vet go-lint go-arch go-test go-build go-evaluate go-check

setup:  ; uv sync --extra dev
fmt:    ; uv run ruff format src tests scripts && uv run ruff check --fix src tests scripts
# lint = ruff（PY-1）+ mypy；arch = 依赖边界（入口层单向依赖，见 scripts/arch_check.py）
lint:   ; uv run ruff format --check src tests scripts && uv run ruff check src tests scripts && uv run mypy src/
arch:   ; uv run python scripts/arch_check.py
# test 含离线评测门禁（HR@3>=0.6 / NDCG@5>=0.4 / total_envy<=2，spec/03-oracles.md）
test:   ; uv run pytest tests/ -m "not llm" --tb=short && uv run python -m mutual.cli evaluate --config config/default.yaml --fail-on-gate
build:  ; uv build
check:  lint arch test

# ---------------------------------------------------------------------------
# Go 面（ADR-0027：Go+BAML 重写）。双栈过渡期与 Python 面并存，
# PR3 移除 Python 面后本节成为唯一构建面。
# ---------------------------------------------------------------------------

go-fmt:  ; gofmt -l -w cmd internal config
go-vet:  ; go vet ./...
go-lint: go-vet
go-arch: ; go run ./cmd/archlint
go-test: ; go test ./...
go-build: ; go build ./...
# 离线评测门禁（与 Python `test` 目标同门禁数值，golden 对拍守护等价性）
go-evaluate: ; go run ./cmd/mutual evaluate --config config/default.yaml --fail-on-gate
go-check: go-lint go-arch go-test go-evaluate
