// Package feedback 实现 LLM 自我改进反馈注入（Phase 3，
// docs/engineering-plan.md §5.2；对应 Python src/mutual/feedback.py）。
//
// 三种反馈层级（spec/03-oracles.md §4）：
//
//  1. Prompt 校准（CalibratePrompt）：评测报告的 HR/NDCG 历史写回
//     打分 prompt 头部（few-shot 式信号）；
//  2. 权重校准（CalibrateWeights）：HR 下降时有界步进调整
//     blending.embed_weight / llm_weight（禁止越界翻转）；
//  3. Agent 记忆（MatchMemory）：记录接受/拒绝的匹配对，供后续轮次
//     注入 prompt 或作 novelty 排除。
//
// 全部纯函数 / 内存对象，无 IO；持久化由调用方（pipeline/CLI）决定。
package feedback

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
)

// 权重校准边界与步长（有界，防止校准发散）。
const (
	WMin  = 0.1
	WMax  = 0.9
	WStep = 0.05
)

// CalibratePrompt 把近期评测结果作为校准信号写入 prompt 头部。
//
// history 为空时原样返回；取末尾 maxEntries 条（默认 3）。
func CalibratePrompt(template string, history []domain.EvaluationReport, maxEntries int) string {
	if len(history) == 0 {
		return template
	}
	if maxEntries <= 0 {
		maxEntries = 3
	}
	tail := history
	if len(tail) > maxEntries {
		tail = tail[len(tail)-maxEntries:]
	}
	lines := []string{"[Calibration] Recent evaluation feedback:"}
	for _, r := range tail {
		lines = append(lines, fmt.Sprintf(
			"- HR@3=%.2f NDCG@5=%.2f envy=%d quality=%s; "+
				"reward reciprocal pairs where BOTH directions are strong; "+
				"penalize one-directional attraction.",
			r.HRAt3, r.NDCGAt5, r.TotalEnvy(), trend(r)))
	}
	return strings.Join(lines, "\n") + "\n\n" + template
}

// trend 粗粒度质量分档（供 prompt 校准措辞）。
func trend(r domain.EvaluationReport) string {
	if r.HRAt3 >= 0.8 {
		return "high"
	}
	if r.HRAt3 >= 0.5 {
		return "medium"
	}
	return "low"
}

// CalibrateWeights 按 HR 变化调整 embed/llm 混合权重
// （spec/03-oracles.md §4：HR 下降时触发）。
//
// 规则（保守有界）：无历史或 HR 持平/上升 → 不动；HR 下降 →
// llm_weight += step、embed_weight -= step，重归一化并截断到边界。
// 不修改入参。
func CalibrateWeights(blending engine.BlendingConfig, current, previous *domain.EvaluationReport, step float64) engine.BlendingConfig {
	if step <= 0 {
		step = WStep
	}
	// current/previous 均可为 nil（首轮尚无报告）：一致防护，避免空指针
	// 中断校准闭环（CodeRabbit）。
	if current == nil || previous == nil {
		return blending
	}
	if current.HRAt3 >= previous.HRAt3 {
		return blending // 未下降：不触发
	}
	ew := blending.EmbedWeight - step
	lw := blending.LLMWeight + step
	ew = clamp(ew, WMin, WMax)
	lw = clamp(lw, WMin, WMax)
	// 重归一化（和为 1；越界截断导致和≠1 时按比例缩放）。
	total := ew + lw
	if total > 0 {
		ew, lw = ew/total, lw/total
	}
	return engine.BlendingConfig{EmbedWeight: ew, LLMWeight: lw}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------------------
// 3. Agent 记忆（接受/拒绝的匹配）
// ---------------------------------------------------------------------------

// MemoryEntry 是单条匹配反馈记录。
type MemoryEntry struct {
	PairID   string `json:"pair_id"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// MatchMemory 是跨轮匹配反馈记忆（接受/拒绝），
// 供 prompt 注入或 novelty 排除。
type MatchMemory struct {
	Entries []MemoryEntry
}

// Record 记录一次匹配反馈。
func (m *MatchMemory) Record(pairID string, accepted bool, reason string) {
	m.Entries = append(m.Entries, MemoryEntry{PairID: pairID, Accepted: accepted, Reason: reason})
}

// RejectedPairIDs 被拒绝的 pair_id（可并入 excluded pairs 做 novelty 排除）。
func (m *MatchMemory) RejectedPairIDs() []string {
	out := make([]string, 0, len(m.Entries))
	for _, e := range m.Entries {
		if !e.Accepted {
			out = append(out, e.PairID)
		}
	}
	return out
}

// PromptBlock 近期反馈的 prompt 注入块（空记忆返回空串）。
func (m *MatchMemory) PromptBlock(maxEntries int) string {
	if len(m.Entries) == 0 {
		return ""
	}
	if maxEntries <= 0 {
		maxEntries = 5
	}
	tail := m.Entries
	if len(tail) > maxEntries {
		tail = tail[len(tail)-maxEntries:]
	}
	lines := []string{"[Memory] Recent match feedback:"}
	for _, e := range tail {
		verdict := "REJECTED"
		if e.Accepted {
			verdict = "ACCEPTED"
		}
		reason := ""
		if e.Reason != "" {
			reason = " — " + e.Reason
		}
		lines = append(lines, fmt.Sprintf("- %s: %s%s", e.PairID, verdict, reason))
	}
	return strings.Join(lines, "\n")
}

// Save 持久化到 JSONL（整写；append 语义由调用方控制）。
func (m *MatchMemory) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建记忆目录: %w", err)
	}
	var b strings.Builder
	for _, e := range m.Entries {
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("序列化记忆条目: %w", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// LoadMatchMemory 从 JSONL 恢复；文件不存在返回空记忆。
func LoadMatchMemory(path string) (*MatchMemory, error) {
	mem := &MatchMemory{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return mem, nil
		}
		return nil, fmt.Errorf("读取记忆文件: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e MemoryEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("解析记忆行 %q: %w", line, err)
		}
		mem.Entries = append(mem.Entries, e)
	}
	return mem, nil
}
