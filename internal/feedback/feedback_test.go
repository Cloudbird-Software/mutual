package feedback

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
)

// TestCalibratePromptEmptyHistory 历史为空时原样返回（无校准信号）。
func TestCalibratePromptEmptyHistory(t *testing.T) {
	tpl := "Score this match."
	if got := CalibratePrompt(tpl, nil, 3); got != tpl {
		t.Errorf("空历史应原样返回: got %q", got)
	}
}

// TestCalibratePromptTail 取末尾 maxEntries 条（默认 3）。
func TestCalibratePromptTail(t *testing.T) {
	history := make([]domain.EvaluationReport, 0, 5)
	for i := 0; i < 5; i++ {
		history = append(history, domain.EvaluationReport{
			HRAt3:   float64(i) / 10,
			NDCGAt5: float64(i) / 10,
		})
	}
	got := CalibratePrompt("TPL", history, 3)
	if !strings.HasPrefix(got, "[Calibration] Recent evaluation feedback:") {
		t.Errorf("校准头缺失: %q", got)
	}
	if !strings.HasSuffix(got, "\n\nTPL") {
		t.Errorf("模板应在校准块之后: %q", got)
	}
	// 只含末尾 3 条（HR@3 = 0.2/0.3/0.4），不含前两条。
	for _, frag := range []string{"HR@3=0.20", "HR@3=0.30", "HR@3=0.40"} {
		if !strings.Contains(got, frag) {
			t.Errorf("应含末尾条目 %s", frag)
		}
	}
	if strings.Contains(got, "HR@3=0.00") || strings.Contains(got, "HR@3=0.10") {
		t.Error("不应含超出 maxEntries 的旧条目")
	}
}

// TestCalibrateWeightsNoPrevious 无前一轮 → 不动（校准需要对比基线）。
func TestCalibrateWeightsNoPrevious(t *testing.T) {
	b := engine.BlendingConfig{EmbedWeight: 0.35, LLMWeight: 0.65}
	cur := &domain.EvaluationReport{HRAt3: 0.4}
	if got := CalibrateWeights(b, cur, nil, 0); got != b {
		t.Errorf("无 previous 应原样返回: got %+v", got)
	}
}

// TestCalibrateWeightsNoDecline HR 持平/上升 → 不动。
func TestCalibrateWeightsNoDecline(t *testing.T) {
	b := engine.BlendingConfig{EmbedWeight: 0.35, LLMWeight: 0.65}
	cur := &domain.EvaluationReport{HRAt3: 0.7}
	prev := &domain.EvaluationReport{HRAt3: 0.6}
	if got := CalibrateWeights(b, cur, prev, 0); got != b {
		t.Errorf("HR 上升不应触发: got %+v", got)
	}
	cur.HRAt3 = 0.6 // 持平
	if got := CalibrateWeights(b, cur, prev, 0); got != b {
		t.Errorf("HR 持平不应触发: got %+v", got)
	}
}

// TestCalibrateWeightsDecline HR 下降 → llm+step、embed-step，重归一化。
func TestCalibrateWeightsDecline(t *testing.T) {
	b := engine.BlendingConfig{EmbedWeight: 0.35, LLMWeight: 0.65}
	cur := &domain.EvaluationReport{HRAt3: 0.5}
	prev := &domain.EvaluationReport{HRAt3: 0.8}
	got := CalibrateWeights(b, cur, prev, 0.05)
	if math.Abs(got.LLMWeight-0.70) > 1e-9 || math.Abs(got.EmbedWeight-0.30) > 1e-9 {
		t.Errorf("HR 下降应 llm+0.05/embed-0.05: got %+v", got)
	}
	if math.Abs(got.LLMWeight+got.EmbedWeight-1) > 1e-9 {
		t.Errorf("权重和应归一化为 1: got %+v", got)
	}
	// 入参不被修改。
	if b.EmbedWeight != 0.35 || b.LLMWeight != 0.65 {
		t.Error("CalibrateWeights 修改了入参")
	}
}

// TestCalibrateWeightsBounded 有界：多次下降校准不越界翻转
// （embed/llm 均被截断在 [0.1, 0.9]）。
func TestCalibrateWeightsBounded(t *testing.T) {
	b := engine.BlendingConfig{EmbedWeight: 0.35, LLMWeight: 0.65}
	cur := &domain.EvaluationReport{HRAt3: 0.1}
	prev := &domain.EvaluationReport{HRAt3: 0.9}
	for i := 0; i < 50; i++ {
		b = CalibrateWeights(b, cur, prev, 0.05)
		if b.EmbedWeight < WMin-1e-9 || b.EmbedWeight > WMax+1e-9 {
			t.Fatalf("embed_weight 越界: %+v", b)
		}
		if b.LLMWeight < WMin-1e-9 || b.LLMWeight > WMax+1e-9 {
			t.Fatalf("llm_weight 越界: %+v", b)
		}
		if math.Abs(b.EmbedWeight+b.LLMWeight-1) > 1e-9 {
			t.Fatalf("归一化破坏: %+v", b)
		}
	}
}

// TestMatchMemory 记录/拒绝集/prompt 注入块。
func TestMatchMemory(t *testing.T) {
	var m MatchMemory
	m.Record("alice__bob", true, "")
	m.Record("carol__david", false, "no skill overlap")
	m.Record("alice__carol", false, "")

	rejected := m.RejectedPairIDs()
	if len(rejected) != 2 || rejected[0] != "carol__david" || rejected[1] != "alice__carol" {
		t.Errorf("RejectedPairIDs: got %v", rejected)
	}

	block := m.PromptBlock(3)
	if !strings.HasPrefix(block, "[Memory] Recent match feedback:") {
		t.Errorf("记忆头缺失: %q", block)
	}
	if !strings.Contains(block, "alice__bob: ACCEPTED") ||
		!strings.Contains(block, "carol__david: REJECTED — no skill overlap") {
		t.Errorf("记忆条目: %q", block)
	}

	var empty MatchMemory
	if empty.PromptBlock(5) != "" {
		t.Error("空记忆应返回空串")
	}
}

// TestMatchMemoryPersistence Save → Load 往返一致。
func TestMatchMemoryPersistence(t *testing.T) {
	var m MatchMemory
	m.Record("alice__bob", true, "")
	m.Record("carol__david", false, "no skill overlap")

	path := filepath.Join(t.TempDir(), "sub", "memory.jsonl")
	if err := m.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadMatchMemory(path)
	if err != nil {
		t.Fatalf("LoadMatchMemory: %v", err)
	}
	if len(got.Entries) != len(m.Entries) {
		t.Fatalf("条目数: got %d want %d", len(got.Entries), len(m.Entries))
	}
	for i := range m.Entries {
		if got.Entries[i] != m.Entries[i] {
			t.Errorf("条目 %d: got %+v want %+v", i, got.Entries[i], m.Entries[i])
		}
	}
}

// TestLoadMatchMemoryMissing 文件不存在 → 空记忆（首跑语义）。
func TestLoadMatchMemoryMissing(t *testing.T) {
	m, err := LoadMatchMemory(filepath.Join(t.TempDir(), "nonexistent.jsonl"))
	if err != nil || len(m.Entries) != 0 {
		t.Errorf("缺失文件: got %v err=%v", m, err)
	}
}

// TestLoadMatchMemoryBadLine 坏行 fail-loud（记忆是校准信号源，
// 静默吞错会让校准建立在残缺历史上）。
func TestLoadMatchMemoryBadLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.jsonl")
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o644); err != nil {
		t.Fatalf("写入坏文件: %v", err)
	}
	if _, err := LoadMatchMemory(path); err == nil {
		t.Fatal("坏行应报错")
	}
}
