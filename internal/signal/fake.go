// Fake 是离线 golden test 用的确定性替身（spec/04-fixtures.md §7）。
//
// 契约（变更契约 = spec 变更，由测试守护）：
//   - FakeLLM：打分调用 → 按 prompt 中出现的 cohort id 查表（§7.1）；
//     非打分调用 → 固定 intro JSON（§7.1"否则 → 非打分类路径"）。
//     Go 侧按阶段类型化分发（engine.LLMClient 四方法），可观察行为
//     与 §7.1 的 Python 内容路由逐位一致。
//   - FakeEmbedder：每条文本独立 RandomState(hash_text(t) % 2^32)
//     产 128 维 randn——content-addressed（同文本同向量，跨 run 稳定）。
//
// 与 Python tests/conftest.py 的 fake_llm / FakeEmbedder 逐位对齐。
package signal

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"

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

// fakeCohortIDs 是 spec/04-fixtures.md §7.1 记录的 cohort id 全集
// （历史全文路由语义；现行路由只认块头结构化位置，见 CompleteScore）。
var fakeCohortIDs = []string{"alice", "bob", "carol", "david"}

// FakeLLM 满足 engine.LLMClient（按阶段类型化的四方法）。
type FakeLLM struct {
	// CallCount 是累计调用次数（缓存命中率断言用）。
	CallCount int
}

// defaultScoreResponse 是查表未命中时的兜底打分响应。
const defaultScoreResponse = `{"a_to_b": 0.5, "b_to_a": 0.5, "reasoning": "fake"}`

// introResponse 是非打分路径的固定响应（§7.1"否则 → 非打分类路径"）。
const introResponse = `{"intro": "Fake intro.", "starter_topics": "Fake topic."}`

// CompleteScore 打分类路径：按 prompt 中出现的 cohort id 查表（§7.1）。
//
// 路由语义收紧（RT-2026-08 #35）：查表只认 "### Pair N: (u1, u2)" 块头
// 的**结构化位置**（engine.buildScoringPrompt 对单块也渲染块头）；
// 无块头的 prompt 返回兜底 0.5/0.5——不再做全文 Contains 搜索（画像
// 文本写 "alice bob" 即可命中预构建高分表的劫持面）。golden 分数
// 不受影响（golden 全部走块头路径，钉的是分数不是路由实现）。
func (f *FakeLLM) CompleteScore(prompt string, model string) (string, error) {
	_ = model
	f.CallCount++
	blocks := pairBlockRE.FindAllStringSubmatch(prompt, -1)
	if len(blocks) == 0 {
		return defaultScoreResponse, nil
	}
	objs := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		objs = append(objs, scoreByPairIDs(b[1], b[2]))
	}
	if len(objs) == 1 {
		out, _ := json.Marshal(objs[0])
		return string(out), nil
	}
	out, _ := json.Marshal(objs)
	return string(out), nil
}

// pairBlockRE 匹配批量打分 prompt 的分对标记："### Pair 2: (alice, bob)"
// （与 engine.buildScoringPrompt 的块头一致）。
var pairBlockRE = regexp.MustCompile(`(?m)^### Pair \d+: \(([^,\s]+), ([^)\s]+)\)$`)

// scoreByPairIDs 按一对 user id 查表：排序后拼 key，命中返回表值，
// 未命中返回兜底 0.5/0.5（与整段查表同语义，不做方向交换——
// Python conftest 的查表即如此）。
func scoreByPairIDs(u1, u2 string) map[string]any {
	ids := []string{u1, u2}
	sort.Strings(ids)
	if entry, ok := fakeScoreTable[ids[0]+"__"+ids[1]]; ok {
		return map[string]any{"a_to_b": entry[0], "b_to_a": entry[1], "reasoning": "fake"}
	}
	return map[string]any{"a_to_b": 0.5, "b_to_a": 0.5, "reasoning": "fake"}
}

// CompleteExtract 非打分类路径：固定话术 JSON（§7.1——extract 拿到
// 该响应会全分节退化，golden 约定里 extract/hyde 由 scripted 替身接管）。
func (f *FakeLLM) CompleteExtract(prompt string, model string) (string, error) {
	_ = prompt
	_ = model
	f.CallCount++
	return introResponse, nil
}

// CompleteHyde 非打分类路径：固定话术 JSON（§7.1）。
func (f *FakeLLM) CompleteHyde(prompt string, model string) (string, error) {
	_ = prompt
	_ = model
	f.CallCount++
	return introResponse, nil
}

// CompleteIntroduce 话术路径：固定 intro JSON（§7.1）。
func (f *FakeLLM) CompleteIntroduce(prompt string, model string) (string, error) {
	_ = prompt
	_ = model
	f.CallCount++
	return introResponse, nil
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
	// bitSize=32：解析结果 ≤ math.MaxUint32，转 uint32 无截断
	// （CodeQL go/incorrect-integer-conversion：64 位解析后截断有
	// 溢出隐患；8 位 hex 上限恰为 0xFFFFFFFF，32 位解析等价且无损）。
	v, err := strconv.ParseUint(hash, 16, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}
