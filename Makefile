.PHONY: setup fmt lint arch test build check

setup:  ; uv sync --extra dev
fmt:    ; uv run ruff format src tests scripts && uv run ruff check --fix src tests scripts
# lint = ruff（PY-1）+ mypy；arch = 依赖边界（入口层单向依赖，见 scripts/arch_check.py）
lint:   ; uv run ruff format --check src tests scripts && uv run ruff check src tests scripts && uv run mypy src/
arch:   ; uv run python scripts/arch_check.py
# test 含离线评测门禁（HR@3>=0.6 / NDCG@5>=0.4 / total_envy<=2，spec/03-oracles.md）
test:   ; uv run pytest tests/ -m "not llm" --tb=short && uv run python -m mutual.cli evaluate --config config/default.yaml --fail-on-gate
build:  ; uv build
check:  lint arch test
