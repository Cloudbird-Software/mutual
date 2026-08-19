// Package store 是管线适配层的存储抽象（对应 Python src/mutual/store.py）。
//
// 分层契约（CLAUDE.md §2.3 铁律）：engine 的核心阶段是纯变换，不 import
// 本包；只有 pipeline（runners）通过 Store 接口做 IO。
//
// FileStore 目录结构：
//
//	{root}/
//	├── raw/         # 原始 Profile（外部注入）
//	├── processed/   # ExtractedSections（processed/sections/{id}.json）
//	├── embeds/      # EmbeddingsBundle（embeds/bundle.json）
//	├── outputs/     # 匹配结果 / 报告
//	├── cache/       # LLM 响应缓存（content-addressed）
//	└── match_history.jsonl  # append-only，novelty 排除的数据源
//
// 序列化说明（与 Python 基线的有意分歧）：Python 侧 bundle 用 npz；
// Go 侧用单一 JSON 文件（自包含、人/AI 可读、跨平台确定性）。
// bundle 的磁盘格式是 adapter 细节，不属于核心契约（spec/01-schemas.md）。
package store

import (
	"regexp"
	"strings"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// safeIDRe 是 ID 的安全白名单（路径穿越守卫）：单段、字母数字开头、
// 仅含字母数字与 ._-。
var safeIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// SafeFilename 校验 ID 可安全用作文件名；不安全返回 false。
//
// 规则：非空、仅字母数字与 ._-、不得以点开头（.. / 隐藏文件）、
// 不得含路径分隔符（正则已排除 / 与 \）、不得含连续点（双保险）。
func SafeFilename(userID string) bool {
	if !safeIDRe.MatchString(userID) {
		return false
	}
	return !strings.Contains(userID, "..")
}

// MatchRecord 是 match_history.jsonl 的单行记录。
type MatchRecord struct {
	PairID    domain.PairID `json:"pair_id"`
	User1     domain.UserID `json:"user1"`
	User2     domain.UserID `json:"user2"`
	MatchedAt string        `json:"matched_at"` // ISO-8601；缺失/不可解析时读侧保守保留
}

// ToMap 与 Python 侧记录形状一致（JSONL 行）。
func (r MatchRecord) ToMap() map[string]any {
	return map[string]any{
		"pair_id":    string(r.PairID),
		"user1":      string(r.User1),
		"user2":      string(r.User2),
		"matched_at": r.MatchedAt,
	}
}

// Store 是存储抽象：engine 核心不依赖任何具体实现，
// pipeline 通过此接口做 IO。
type Store interface {
	// GetSections 读取已提取的 sections；userIDs 为 nil 表示全部。
	// 缺失 id 不是错误（读到什么给什么）。
	GetSections(userIDs []domain.UserID) (map[domain.UserID]domain.ExtractedSections, error)

	// PutSections 持久化 sections。全部分节均为 NotSpecified 的
	// 失败提取不落盘（spec/05-boundaries.md §4，否则永远不会重试）。
	PutSections(extracted []domain.ExtractedSections) error

	// GetEmbeddings 读取已有 bundle；不存在返回 (nil, nil)。
	// embedding_model 一致性守卫由 embed 阶段执行（§6）。
	GetEmbeddings() (*domain.EmbeddingsBundle, error)

	// PutEmbeddings 持久化 bundle（全尺寸存储；MRL 截断在计算时做）。
	PutEmbeddings(bundle *domain.EmbeddingsBundle) error

	// GetMatchHistory 读取窗口内的匹配历史（novelty 排除的数据源，
	// spec/05-boundaries.md §8）。
	GetMatchHistory() ([]MatchRecord, error)

	// PutMatches 把本次新匹配边 append 到 match_history.jsonl。
	PutMatches(edges []domain.Edge) error
}
