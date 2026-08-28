// Package signal 提供确定性的 LLM/embedder 替身信号源（离线评测用）。
//
// 两种替身（spec/03-oracles.md §3 的评测 oracle 数据源）：
//   - Surrogate（本文件）：从画像文本计算语义可解释的确定性信号
//     （token 重叠式方向分 / TF 余弦 embedding 分 + 固定 seed 噪声），
//     模拟真实 LLM/embedding 的判断质量；
//   - FakeLLM / FakeEmbedder（fake.go）：按 prompt 查表返回固定响应，
//     守护 stage 契约（spec/04-fixtures.md §7）。
//
// 与 Python 基线（src/mutual/surrogate.py）逐位对齐：随机噪声流
// RandomState(seed).rand() 的消费顺序 = member × pool × (a_to_b, b_to_a)。
package signal

import (
	"math"
	"regexp"
	"sort"

	"github.com/Cloudbird-Software/mutual/internal/num"
	"github.com/Cloudbird-Software/mutual/internal/rng"
)

// 与 config/default.yaml recipe.section_weights 对齐的代理权重。
const (
	wNeedsSkills   = 0.6
	wProjectSkills = 0.2
	wVision        = 0.2
)

// sectionJoinOrder 是画像级 embedding 拼接的分节顺序（Python _SECTION_KEYS）。
var sectionJoinOrder = []string{"needs", "project", "skills", "vision"}

var wordRE = regexp.MustCompile(`[a-z0-9]+`)

// Tokenize 小写化并切词：ASCII 走 [a-z0-9]+ 词切分（与 Python 基线
// 逐位一致，golden 语义不变）；CJK 字符追加二元组（无分词器的廉价
// 语义近似——海外商人↔本地企业等跨语言画像的离线评测可观测性，
// 2026-08 实验 R4：纯 ASCII 切词下中文侧词法全盲，NSW 几何均值
// 湮灭黄金对，LLM 行级 10/10 全解）。
func Tokenize(text string) []string {
	t := lower(text)
	toks := wordRE.FindAllString(t, -1)
	toks = append(toks, cjkBigrams(t)...)
	return toks
}

// cjkBigrams 提取 CJK 统一表意文字区段的字符二元组（单字原样保留）。
// 扩展区（ExtA/Compat/B/C/D）与日文假名不覆盖：合成语料以简中为主，
// 按需扩展。
func cjkBigrams(text string) []string {
	var out []string
	runes := []rune(text)
	i := 0
	for i < len(runes) {
		if !isHan(runes[i]) {
			i++
			continue
		}
		j := i
		for j < len(runes) && isHan(runes[j]) {
			j++
		}
		for k := i; k < j; k++ {
			if k+1 < j {
				out = append(out, string(runes[k:k+2]))
			} else if k == i {
				// 单字 run：保留单字（可与其他 run 的二元组区分开）
				out = append(out, string(runes[k]))
			}
		}
		i = j
	}
	return out
}

func isHan(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK 统一表意文字
		(r >= 0x3400 && r <= 0x4DBF) // 扩展 A
}

func lower(s string) string {
	// ASCII 快路径覆盖画像文本的约定字符集；非 ASCII 字符按 rune 小写化。
	needRune := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			needRune = true
			break
		}
	}
	if !needRune {
		b := []byte(s)
		for i, c := range b {
			if c >= 'A' && c <= 'Z' {
				b[i] = c + ('a' - 'A')
			}
		}
		return string(b)
	}
	runes := []rune(s)
	for i, r := range runes {
		runes[i] = runeDown(r)
	}
	return string(runes)
}

func runeDown(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// tokenSet 提取某分节的 token 集合（缺失分节 → 空集）。
func tokenSet(sections map[string]string, key string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range Tokenize(sections[key]) {
		out[tok] = true
	}
	return out
}

// overlap 计算集合重叠的余弦式度量 ∈ [0, 1]；双方皆空记 0（中性偏保守）。
func overlap(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for tok := range a {
		if b[tok] {
			inter++
		}
	}
	return float64(inter) / math.Sqrt(float64(len(a)*len(b)))
}

// DirectionalScore 返回 A 对 B 的方向性价值分 ∈ [0, 1]
// （模拟 LLM 的 a_to_b 打分，与 recipe.instruction 语义对齐）：
//
//	0.6 · overlap(A.needs,   B.skills)  —— 需求被技能直击（主信号）
//	0.2 · overlap(A.project, B.skills)  —— 项目协作空间
//	0.2 · overlap(A.vision,  B.vision)  —— 方向一致
func DirectionalScore(sectionsA, sectionsB map[string]string) float64 {
	return wNeedsSkills*overlap(tokenSet(sectionsA, "needs"), tokenSet(sectionsB, "skills")) +
		wProjectSkills*overlap(tokenSet(sectionsA, "project"), tokenSet(sectionsB, "skills")) +
		wVision*overlap(tokenSet(sectionsA, "vision"), tokenSet(sectionsB, "vision"))
}

// tfVectors 构建固定词表 TF 向量并 L2 归一化（确定性，无外部模型）。
//
// 词表按文本顺序的首次出现构建；归一化走 num.Norm（NumPy pairwise
// 求和，逐位对齐 np.linalg.norm）。
func tfVectors(texts []string) [][]float64 {
	vocab := map[string]int{}
	tokenLists := make([][]string, len(texts))
	for r, t := range texts {
		tokenLists[r] = Tokenize(t)
		for _, tok := range tokenLists[r] {
			if _, ok := vocab[tok]; !ok {
				vocab[tok] = len(vocab)
			}
		}
	}
	mat := make([][]float64, len(texts))
	for r := range mat {
		mat[r] = make([]float64, len(vocab))
	}
	for r, toks := range tokenLists {
		for _, tok := range toks {
			mat[r][vocab[tok]]++
		}
	}
	for _, row := range mat {
		norm := num.Norm(row)
		if norm > 1e-12 {
			for d := range row {
				row[d] /= norm
			}
		}
	}
	return mat
}

// EmbedScore 返回画像级 embedding 相似度 ∈ [0, 1]
// （四个分节按固定顺序拼接后 TF 余弦，截断到 [0, 1]）。
func EmbedScore(sectionsA, sectionsB map[string]string) float64 {
	mat := tfVectors([]string{joinSections(sectionsA), joinSections(sectionsB)})
	if len(mat[0]) == 0 {
		return 0
	}
	dot := 0.0
	for d := range mat[0] {
		dot += mat[0][d] * mat[1][d]
	}
	return math.Max(0, math.Min(1, dot))
}

func joinSections(sections map[string]string) string {
	parts := make([]string, 0, len(sectionJoinOrder))
	for _, k := range sectionJoinOrder {
		parts = append(parts, sections[k])
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}

// Noisy 加性噪声并截断到 [0, 1]（固定 seed → 确定性）。
//
// 每次调用消费 RandomState 的一个均匀随机数（与 Python
// np.clip(score + scale*(rng.rand()-0.5), 0, 1) 逐位一致）。
func Noisy(score float64, rs *rng.RandomState, noiseScale float64) float64 {
	return math.Max(0, math.Min(1, score+noiseScale*(rs.Float64()-0.5)))
}

// DirScore 是一对双向分数（a_to_b, b_to_a）。
type DirScore struct {
	AToB float64
	BToA float64
}

// OrderedSections 是保序的分节数据（map 迭代无序，评测链路的噪声
// 流依赖 member/pool 的文件顺序，故用显式顺序结构）。
type OrderedSections struct {
	ID       string
	Sections map[string]string
}

// ScoreMatrix 批量计算双向偏好分（模拟 score 阶段输出，喂给 pre_matrix）。
//
// 随机流消费顺序（与 Python 逐位一致）：member 外层 × pool 内层，
// 每对依次 noisy(a_to_b)、noisy(b_to_a)。embeddingOnly 时退化为纯
// embedding 相似度（冷启动：无打分历史，双向同值）。
//
// 调用方必须传入保序的 members/pool（JSON 文件序）。
func ScoreMatrix(
	members, pool []OrderedSections,
	seed int,
	noiseScale float64,
	embeddingOnly bool,
) map[string]map[string]DirScore {
	rs := rng.New(uint32(mod32(seed)))
	out := map[string]map[string]DirScore{}
	for _, m := range members {
		row := map[string]DirScore{}
		for _, p := range pool {
			if embeddingOnly {
				s := EmbedScore(m.Sections, p.Sections)
				row[p.ID] = DirScore{AToB: Noisy(s, rs, noiseScale), BToA: Noisy(s, rs, noiseScale)}
			} else {
				a2b := DirectionalScore(m.Sections, p.Sections)
				b2a := DirectionalScore(p.Sections, m.Sections)
				row[p.ID] = DirScore{AToB: Noisy(a2b, rs, noiseScale), BToA: Noisy(b2a, rs, noiseScale)}
			}
		}
		out[m.ID] = row
	}
	return out
}

// ScoreMatrixBlended 是真实管线 score 阶段双信号混合的离线镜像：
// pref = embedW·noisy(EmbedScore) + llmW·noisy(DirectionalScore)，双向独立。
//
// 用途：官方 bench 的 ScoreMatrix 只喂方向分（LLM 信号替身），完全绕过
// config blending（embed/llm 权重）——真实管线里 embed 相似度是同义改写
// 盲区的兜底信号（classic m3↔p3 类失效）。本函数让离线评测能执行与
// config/default.yaml 相同的混合语义。
//
// 噪声流：方向分与 ScoreMatrix 逐位一致（member × pool × (a_to_b, b_to_a)）；
// embed 分走独立流（seed+777777，同序）。embedW=0 时输出与 ScoreMatrix
// 完全一致（零值兼容，golden 不受影响）。embeddingOnly 场景不适用
// （冷启动无 LLM 信号可混合），调用方负责分流。
func ScoreMatrixBlended(
	members, pool []OrderedSections,
	seed int,
	noiseScale, embedW, llmW float64,
) map[string]map[string]DirScore {
	out := ScoreMatrix(members, pool, seed, noiseScale, false)
	if embedW <= 0 {
		return out
	}
	rs := rng.New(uint32(uint32(mod32(seed) + 777777)))
	for _, m := range members {
		for _, p := range pool {
			en := Noisy(EmbedScore(m.Sections, p.Sections), rs, noiseScale)
			s := out[m.ID][p.ID]
			out[m.ID][p.ID] = DirScore{
				AToB: clamp01f(embedW*en + llmW*s.AToB),
				BToA: clamp01f(embedW*en + llmW*s.BToA),
			}
		}
	}
	return out
}

func clamp01f(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

func mod32(seed int) int {
	u := uint64(uint32(seed))
	return int(u)
}

// SortedKeys 返回 map 键的排序副本（确定性遍历 helper，测试用）。
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
