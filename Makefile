# ---------------------------------------------------------------------------
# Mutual 唯一构建面：Go + BAML（ADR-0027）。Python 基线已完成差分对拍
# 使命（golden/ 为证），随 PR3 移除。
# 契约：CI-Workflows check.yml（runtime: go）调用 make setup && make check。
# ---------------------------------------------------------------------------

.PHONY: setup fmt lint arch test build evaluate check baml-generate

setup:  ; go mod download
fmt:    ; gofmt -l -w cmd internal config
# lint = go vet（编译器级静态检查；archlint 见 arch 目标）
lint:   ; go vet ./...
# arch = 分层依赖门禁（入口层单向依赖，cmd/archlint）
arch:   ; go run ./cmd/archlint
test:   ; go test ./...
build:  ; go build ./...
# 离线评测门禁（HR@3>=0.6 / NDCG@5>=0.4 / total_envy<=2，spec/03-oracles.md）
evaluate: ; go run ./cmd/mutual evaluate --config config/default.yaml --fail-on-gate
check:  lint arch test evaluate

# baml-generate 重生成 BAML 客户端（prompt 契约变更时用，版本与
# baml_src/generators.baml 的 version 一致；变更须同步 golden/baml/ 快照）
baml-generate: ; npx -y @boundaryml/baml@0.226.1 generate && goimports -w baml_client/
