package engine

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// ScoreBudgets 是 LLM 打分阶段的预算（config["budgets"] + models.pair_llm）。
type ScoreBudgets struct {
	// PerProfileCap: 每用户最多打分对数
	// （max_n_llm_evaluations_per_profile）；nil = 不限。
	PerProfileCap *int
	// MaxCalls: 全局 LLM 调用上限（max_pair_llm_calls）；nil = 不限。
	MaxCalls *int
	// BatchSize: 一次 prompt 打几对（n_profiles_to_score_together，
	// 下限 1）。
	BatchSize int
	// Model: 打分模型（models.pair_llm）；空串 = 实现默认。
	Model string
}

// ScoreResult 是 ScorePairs 的输出：按 pair_id 索引的打分表 +
// 保序键列表（Python dict 的插入序 = select 顺序，golden 对拍依赖）。
type ScoreResult struct {
	// Order 是 pair_id 的插入序（去重后的 select 顺序）。
	Order []domain.PairID
	// ByID 按 pair_id 索引 PairScore（含未打分候选，§3）。
	ByID map[domain.PairID]domain.PairScore
}

// Get 按插入序返回全部 PairScore。
func (r *ScoreResult) All() []domain.PairScore {
	out := make([]domain.PairScore, 0, len(r.Order))
	for _, id := range r.Order {
		out = append(out, r.ByID[id])
	}
	return out
}

// ScorePairs 用 LLM 对候选对做双向打分（score 阶段，纯变换）。
//
// 边界（spec/05-boundaries.md）：
//   - §3 未打分候选（预算耗尽 / 批次失败 / 解析失败）保留
//     embedding-only 权重，经 unscored 返回，不静默丢弃；
//   - §5 缓存由 LLMClient 实现负责（按完整 prompt 的 content hash）；
//   - 去重：同 pair_id 保留首个（select 顺序），保证结果确定性。
//
// 解析契约（qodo #3 位置对齐）：响应与 batch 按位置对齐；畸形元素
// 保留为 nil 槽位，不压缩列表——否则左移会记错 pair。单对 batch
// 接受单 JSON 对象（或数组首元素）；多对 batch 只接受 JSON 数组。
func ScorePairs(
	selectedPairs []domain.CandidatePair,
	sectionsDict map[domain.UserID]map[string]string,
	instruction string,
	promptTemplate string,
	llm LLMClient,
	budgets ScoreBudgets,
) (result *ScoreResult, unscored []domain.CandidatePair) {
	batchSize := budgets.BatchSize
	if batchSize < 1 {
		batchSize = 1
	}

	// 去重（同 pair_id 保留首个），保持 select 顺序 → 确定性。
	unique := map[domain.PairID]domain.CandidatePair{}
	var order []domain.PairID
	for _, pair := range selectedPairs {
		if _, dup := unique[pair.PairID]; dup {
			continue
		}
		unique[pair.PairID] = pair
		order = append(order, pair.PairID)
	}

	// 每用户预算：预算不足的 pair 直接记为未打分。
	evalsPerUser := map[domain.UserID]int{}
	unscoredIDs := map[domain.PairID]bool{}
	var admissible []domain.CandidatePair
	for _, id := range order {
		pair := unique[id]
		if budgets.PerProfileCap != nil &&
			(evalsPerUser[pair.User1] >= *budgets.PerProfileCap ||
				evalsPerUser[pair.User2] >= *budgets.PerProfileCap) {
			unscoredIDs[pair.PairID] = true
			continue
		}
		admissible = append(admissible, pair)
		evalsPerUser[pair.User1]++
		evalsPerUser[pair.User2]++
	}

	type dirScore = struct{ a, b float64 }
	scored := map[domain.PairID]dirScore{}
	callsMade := 0
	for start := 0; start < len(admissible); start += batchSize {
		batch := admissible[start:min(start+batchSize, len(admissible))]
		if budgets.MaxCalls != nil && callsMade >= *budgets.MaxCalls {
			for _, pair := range batch {
				unscoredIDs[pair.PairID] = true
			}
			continue
		}
		prompt := buildScoringPrompt(batch, sectionsDict, instruction, promptTemplate)
		raw, err := llm.CompleteScore(prompt, budgets.Model)
		callsMade++
		var parsed []*dirScore
		if err == nil && raw != "" {
			parsed = parseScoringResponse(raw, len(batch))
		}
		for idx, pair := range batch {
			var got *dirScore
			if idx < len(parsed) {
				got = parsed[idx]
			}
			if got == nil {
				unscoredIDs[pair.PairID] = true
			} else {
				scored[pair.PairID] = dirScore{a: got.a, b: got.b}
			}
		}
	}

	byID := make(map[domain.PairID]domain.PairScore, len(order))
	for _, id := range order {
		pair := unique[id]
		ps := domain.PairScore{
			PairID:     pair.PairID,
			User1:      pair.User1,
			User2:      pair.User2,
			EmbedScore: pair.SimilarityScore,
		}
		if s, ok := scored[id]; ok {
			a, b := s.a, s.b
			ps.LLMScoreAToB = &a
			ps.LLMScoreBToA = &b
			fused := fuseDirectional(&a, &b)
			ps.LLMScore = &fused
		}
		byID[id] = ps
	}

	for _, id := range order {
		if unscoredIDs[id] {
			unscored = append(unscored, unique[id])
		}
	}
	return &ScoreResult{Order: order, ByID: byID}, unscored
}

// PrepareNormalizedScores 跨批次稳定归一化 embed/llm 分数
// （reference 分布优先，None 用当前批次统计量；退化分布 → 0.5 中性）。
//
// reference 的解释（spec 沉默 S2）：单一数组 = embed 参考分布；
// {"embed": arr, "llm": arr} = 分分量参考。Go 侧以两个切片显式表达。
func PrepareNormalizedScores(scores *ScoreResult, refEmbed, refLLM []float64) *ScoreResult {
	if scores == nil || len(scores.Order) == 0 {
		return scores
	}

	var embedVals, llmVals []float64
	for _, id := range scores.Order {
		ps := scores.ByID[id]
		embedVals = append(embedVals, ps.EmbedScore)
		if ps.LLMScore != nil {
			llmVals = append(llmVals, *ps.LLMScore)
		}
	}

	out := &ScoreResult{Order: scores.Order, ByID: make(map[domain.PairID]domain.PairScore, len(scores.Order))}
	for _, id := range scores.Order {
		ps := scores.ByID[id]
		embedNorm := minMaxNormalize(ps.EmbedScore, embedVals, refEmbed)
		ps.EmbedScoreNormalized = &embedNorm
		if ps.LLMScore != nil {
			llmNorm := minMaxNormalize(*ps.LLMScore, llmVals, refLLM)
			ps.LLMScoreNormalized = &llmNorm
		}
		out.ByID[id] = ps
	}
	return out
}

// BuildPrefMatrix 把 PairScore 的方向性分数填入双向偏好矩阵
// （pre_matrix 阶段，同集 N×N 方阵模式）。
//
// 填充规则：a_to_b → pref_lr[i][j] 与 pref_rl[i][j]；b_to_a →
// pref_lr[j][i] 与 pref_rl[j][i]（互补单元格由同一对相反方向填充）。
// 缺失 LLM 分数用 embed_score 兜底（embedding 无方向 → 双向同值）；
// 无 PairScore 的对与对角线为 0。
func BuildPrefMatrix(scores *ScoreResult, allUserIDs []domain.UserID) *domain.PrefMatrix {
	ids := dedupeOrdered(allUserIDs)
	index := make(map[domain.UserID]int, len(ids))
	for i, uid := range ids {
		index[uid] = i
	}
	pm := domain.NewPrefMatrix(ids, ids)

	for _, id := range scores.Order {
		ps := scores.ByID[id]
		i, okI := index[ps.User1]
		j, okJ := index[ps.User2]
		if !okI || !okJ || i == j {
			continue
		}
		aVal, bVal := directionalOrEmbed(ps)
		pm.PrefLeftToRight[i][j] = aVal
		pm.PrefLeftToRight[j][i] = bVal
		pm.PrefRightToLeft[j][i] = bVal
		pm.PrefRightToLeft[i][j] = aVal
	}
	return pm
}

// BuildBipartitePrefMatrix 构建 member×pool 二部图偏好矩阵
// （batch/query 模式，M×N 矩形）。
//
// 填充规则：user1 在左侧、user2 在右侧 → pref_lr[i][j]=a_to_b、
// pref_rl[j][i]=b_to_a；反向按分数方向对调；左右同 id（自配对）不填。
func BuildBipartitePrefMatrix(scores *ScoreResult, leftIDs, rightIDs []domain.UserID) *domain.PrefMatrix {
	leftIndex := dedupeOrdered(leftIDs)
	rightIndex := dedupeOrdered(rightIDs)
	leftPos := make(map[domain.UserID]int, len(leftIndex))
	for i, uid := range leftIndex {
		leftPos[uid] = i
	}
	rightPos := make(map[domain.UserID]int, len(rightIndex))
	for j, uid := range rightIndex {
		rightPos[uid] = j
	}
	pm := domain.NewPrefMatrix(leftIndex, rightIndex)

	for _, id := range scores.Order {
		ps := scores.ByID[id]
		// 自配对（左右同 id）不填，先于方向判定（CodeRabbit：原先放在
		// 分支内，forward 命中后 continue 会跳过 reverse 分支——batch
		// 模式 leftIDs ⊆ rightIDs，集合重叠是常态，双向都可能命中）。
		if ps.User1 == ps.User2 {
			continue
		}
		aVal, bVal := directionalOrEmbed(ps)
		// 正向：user1 在左、user2 在右。
		if i, okI := leftPos[ps.User1]; okI {
			if j, okJ := rightPos[ps.User2]; okJ {
				pm.PrefLeftToRight[i][j] = aVal
				pm.PrefRightToLeft[j][i] = bVal
			}
		}
		// 反向：user2 在左、user1 在右（分数方向对调）。
		if i, okI := leftPos[ps.User2]; okI {
			if j, okJ := rightPos[ps.User1]; okJ {
				pm.PrefLeftToRight[i][j] = bVal
				pm.PrefRightToLeft[j][i] = aVal
			}
		}
	}
	return pm
}

// directionalOrEmbed 返回 (a_to_b, b_to_a)；缺失方向用 embed 兜底
// （embedding 无方向 → 双向同值）。
func directionalOrEmbed(ps domain.PairScore) (float64, float64) {
	aVal := ps.EmbedScore
	if ps.LLMScoreAToB != nil {
		aVal = *ps.LLMScoreAToB
	}
	bVal := ps.EmbedScore
	if ps.LLMScoreBToA != nil {
		bVal = *ps.LLMScoreBToA
	}
	return aVal, bVal
}

// fuseDirectional 融合双向分数：已有方向的算术平均（spec 沉默 S3）。
func fuseDirectional(a, b *float64) float64 {
	var vals []float64
	if a != nil {
		vals = append(vals, *a)
	}
	if b != nil {
		vals = append(vals, *b)
	}
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// minMaxNormalize min-max 归一化；reference 优先；退化分布 → 0.5。
func minMaxNormalize(value float64, batchValues, reference []float64) float64 {
	vals := batchValues
	if len(reference) > 0 {
		vals = reference
	}
	if len(vals) == 0 {
		return 0.5
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if hi <= lo {
		return 0.5
	}
	normalized := (value - lo) / (hi - lo)
	return math.Min(1.0, math.Max(0.0, normalized))
}

// buildScoringPrompt 构造批量打分 prompt：一次打 len(batch) 对。
// 打分类 prompt 必含输出格式标记 a_to_b（fake 路由规则，
// spec/04-fixtures.md §7.1）。批量（>1 对）时要求 JSON 数组响应。
func buildScoringPrompt(
	batch []domain.CandidatePair,
	sectionsDict map[domain.UserID]map[string]string,
	instruction string,
	promptTemplate string,
) string {
	blocks := make([]string, 0, len(batch))
	for idx, pair := range batch {
		rendered := pyFormatMap(promptTemplate, map[string]string{
			"user1_sections": FormatSections(sectionsDict[pair.User1]),
			"user2_sections": FormatSections(sectionsDict[pair.User2]),
			"instruction":    instruction,
			"user1":          string(pair.User1),
			"user2":          string(pair.User2),
		})
		blocks = append(blocks, "### Pair "+strconv.Itoa(idx+1)+": ("+string(pair.User1)+", "+string(pair.User2)+")\n"+rendered)
	}
	if len(batch) == 1 {
		return blocks[0]
	}
	header := "Score each of the " + strconv.Itoa(len(batch)) + " pairs below, in both directions. " +
		"Respond ONLY with a JSON array of exactly " + strconv.Itoa(len(batch)) +
		" objects, in order, each of the form " +
		`{"a_to_b": <float 0.0-1.0>, "b_to_a": <float 0.0-1.0>, "reasoning": "<brief>"}.`
	return header + "\n\n" + strings.Join(blocks, "\n\n")
}

// parseScoringResponse 解析 LLM 打分响应 → 与 batch 按序对齐的
// (a_to_b, b_to_a) 列表；无法解析的槽位为 nil（不压缩，qodo #3）。
func parseScoringResponse(text string, expectedPairs int) []*struct{ a, b float64 } {
	obj := loadsLenient(text)
	var items []any
	switch v := obj.(type) {
	case []any:
		items = v
	case map[string]any:
		if expectedPairs > 1 {
			return nil
		}
		items = []any{v}
	default:
		return nil
	}
	if expectedPairs == 1 && len(items) > 1 {
		items = items[:1]
	}

	type ds = struct{ a, b float64 }
	out := make([]*ds, 0, len(items))
	for _, item := range items {
		d, ok := item.(map[string]any)
		if !ok {
			out = append(out, nil)
			continue
		}
		a := clamp01(d["a_to_b"])
		b := clamp01(d["b_to_a"])
		if a == nil || b == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, &ds{a: *a, b: *b})
	}
	return out
}

// stripMarkdownFence 剥离首尾空白与 markdown 代码围栏（打分与话术
// 两个响应解析器共用的容错前处理）。
func stripMarkdownFence(text string) string {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i != -1 {
			s = s[i+1:]
		}
		s = strings.TrimRight(s, " \t\r\n")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimRight(s, " \t\r\n")
	}
	return s
}

// loadsLenient 容忍 markdown 代码围栏与前后噪声的 JSON 解析：
// 先整串尝试，再截取首个 [ 到末个 ] / 首个 { 到末个 }。
func loadsLenient(text string) any {
	s := stripMarkdownFence(text)
	if v, ok := loads(s); ok {
		return v
	}
	for _, pair := range [][2]byte{{'[', ']'}, {'{', '}'}} {
		start := strings.IndexByte(s, pair[0])
		end := strings.LastIndexByte(s, pair[1])
		if start != -1 && end > start {
			if v, ok := loads(s[start : end+1]); ok {
				return v
			}
		}
	}
	return nil
}

func loads(s string) (any, bool) {
	if s == "" {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, false
	}
	return v, true
}

// clamp01 把值截断到 [0,1]；bool / 不可转数值 / NaN → nil。
// 与 Python 一致：接受数值字符串（float("0.8") 行为）。
func clamp01(v any) *float64 {
	switch x := v.(type) {
	case bool:
		return nil
	case float64:
		if math.IsNaN(x) {
			return nil
		}
		f := clampUnit(x)
		return &f
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil || math.IsNaN(f) {
			return nil
		}
		f = clampUnit(f)
		return &f
	default:
		return nil
	}
}

// clampUnit 截断浮点值到 [0,1]（clamp01 的两个分支共用）。
func clampUnit(f float64) float64 {
	return math.Max(0.0, math.Min(1.0, f))
}

// dedupeOrdered 去重保序。
func dedupeOrdered(ids []domain.UserID) []domain.UserID {
	seen := make(map[domain.UserID]bool, len(ids))
	out := make([]domain.UserID, 0, len(ids))
	for _, uid := range ids {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out
}
