// Fake 是离线 golden test 用的确定性替身（spec/04-fixtures.md §7）。
//
// 契约（变更契约 = spec 变更，由测试守护）：
//   - FakeLLM：prompt 含 "a_to_b" → 打分路径（按 prompt 中出现的
//     cohort id 查表，§7.1）；否则 → 话术路径（固定 intro JSON）。
//   - FakeEmbedder：每条文本独立 RandomState(hash_text(t) % 2^32)
//     产 128 维 randn——content-addressed（同文本同向量，跨 run 稳定）。
//
// 与 Python tests/conftest.py 的 fake_llm / FakeEmbedder 逐位对齐。
package signal

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/rng"
)

// FakeDim 是 FakeEmbedder 的向量维度（Python 基线 randn(128)）。
const FakeDim = 128

// fakeScoreTable 是打分分数表（spec/04-fixtures.md §7.1，与
// golden/test_basic/cohort.json 统计自洽）。
var fakeScoreTable = map[string][2]float64{
	"alice__bob":   {0.85, 0.90},
	"alice__carol": {0.80, 0.82},
	"bob__carol":   {0.83, 0.82},
	"alice__david": {0.52, 0.63},
	"bob__david":   {0.45, 0.58},
	"carol__david": {0.35, 0.65},
}

// fakeCohortIDs 是 fake 路由识别的 cohort id 全集。
var fakeCohortIDs = []string{"alice", "bob", "carol", "david"}

// FakeLLM 满足 engine.LLMClient（Complete(prompt, model)）。
type FakeLLM struct {
	// CallCount 是累计调用次数（缓存命中率断言用）。
	CallCount int
}

// defaultScoreResponse 是查表未命中时的兜底打分响应。
const defaultScoreResponse = `{"a_to_b": 0.5, "b_to_a": 0.5, "reasoning": "fake"}`

// introResponse 是话术路径的固定响应。
const introResponse = `{"intro": "Fake intro.", "starter_topics": "Fake topic."}`

// Complete 实现 engine.LLMClient：按 prompt 内容路由（§7.1）。
func (f *FakeLLM) Complete(prompt string, model string) (string, error) {
	f.CallCount++
	if strings.Contains(prompt, "a_to_b") {
		return scoringResponse(prompt), nil
	}
	return introResponse, nil
}

// scoringResponse 打分类路径：按 prompt 中出现的 cohort id 查表（§7.1）。
func scoringResponse(prompt string) string {
	var found []string
	for _, id := range fakeCohortIDs {
		if strings.Contains(prompt, id) {
			found = append(found, id)
		}
	}
	sort.Strings(found)
	if len(found) >= 2 {
		if entry, ok := fakeScoreTable[found[0]+"__"+found[1]]; ok {
			out, _ := json.Marshal(map[string]any{
				"a_to_b": entry[0], "b_to_a": entry[1], "reasoning": "fake",
			})
			return string(out)
		}
	}
	return defaultScoreResponse
}

// FakeEmbedder 满足 Embedder 接口：每条文本独立播种的 128 维 randn。
type FakeEmbedder struct{}

// Embed 对一组文本生成向量 [N][128]（content-addressed，确定性）。
//
// 与 Python 逐位一致：
//
//	np.random.RandomState(int(hash_text(t), 16) % 2**32).randn(128)
func (FakeEmbedder) Embed(texts []string) [][]float64 {
	out := make([][]float64, len(texts))
	for i, t := range texts {
		seed := seedFromHash(domain.HashText(t))
		rs := rng.New(seed)
		row := make([]float64, FakeDim)
		for d := range row {
			row[d] = rs.NormFloat64()
		}
		out[i] = row
	}
	return out
}

// seedFromHash 把 hash_text 的十六进制输出折叠成 NumPy 标量 seed
// （int(h, 16) % 2**32 = 低 32 位）。
func seedFromHash(hash string) uint32 {
	// hash_text 输出 32 位 MD5 hex（64 位值内）；从低位段解析。
	const digits = 8
	if len(hash) > digits {
		hash = hash[len(hash)-digits:]
	}
	v, err := strconv.ParseUint(hash, 16, 64)
	if err != nil {
		return 0
	}
	return uint32(v)
}
