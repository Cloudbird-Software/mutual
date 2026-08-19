package engine

import (
	"sort"
	"strconv"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// CreateReport 生成人类可读的匹配报告（report 阶段，纯函数）：
// 每用户 top-N 匹配列表（按 final_weight 降序）+ 群组摘要。
//
// scopeUserIDs 限定报告范围（batch 模式只报 member 侧）；nil 表示
// 全部用户（以 extracted 的 id 集合为全集）。scope 限定的是
// "给谁出报告"；与 scoped 用户相邻的边计入统计，对端只作为
// partner 出现。
//
// 输出结构对齐 golden/test_basic/cohort.json 的形状（golden 差分
// 的比对目标）。
func CreateReport(
	edges []domain.Edge,
	extracted []domain.ExtractedSections,
	topMatchesPerUser int,
	scopeUserIDs []domain.UserID,
) map[string]any {
	var scope []domain.UserID
	if scopeUserIDs != nil {
		scope = append([]domain.UserID(nil), scopeUserIDs...)
	} else {
		scope = make([]domain.UserID, 0, len(extracted))
		for _, es := range extracted {
			scope = append(scope, es.ID)
		}
	}
	scopeSet := make(map[domain.UserID]bool, len(scope))
	for _, uid := range scope {
		scopeSet[uid] = true
	}

	var relevant []domain.Edge
	for _, e := range edges {
		if scopeSet[e.User1] || scopeSet[e.User2] {
			relevant = append(relevant, e)
		}
	}

	byUser := map[domain.UserID][]domain.Edge{}
	for _, uid := range scope {
		byUser[uid] = nil
	}
	for _, edge := range relevant {
		if scopeSet[edge.User1] {
			byUser[edge.User1] = append(byUser[edge.User1], edge)
		}
		if scopeSet[edge.User2] {
			byUser[edge.User2] = append(byUser[edge.User2], edge)
		}
	}

	users := map[string]any{}
	degrees := make([]int, 0, len(scope))
	for _, uid := range scope {
		entries := append([]domain.Edge(nil), byUser[uid]...)
		sort.SliceStable(entries, func(a, b int) bool {
			if entries[a].FinalWeight != entries[b].FinalWeight {
				return entries[a].FinalWeight > entries[b].FinalWeight
			}
			return entries[a].PairID < entries[b].PairID
		})
		capped := entries
		if topMatchesPerUser > 0 {
			capped = entries[:min(topMatchesPerUser, len(entries))]
		}
		matches := make([]any, len(capped))
		for i, e := range capped {
			matches[i] = matchEntry(e, uid)
		}
		users[string(uid)] = map[string]any{
			"degree":  len(entries),
			"matches": matches,
		}
		degrees = append(degrees, len(entries))
	}

	// 度分布：按度数升序聚合 {"<degree>": count}。
	sort.Ints(degrees)
	degreeDist := map[string]int{}
	for _, d := range degrees {
		degreeDist[intKey(d)]++
	}

	totalEdges := len(relevant)
	avgDegree := 0.0
	if len(scope) > 0 {
		sum := 0
		for _, d := range degrees {
			sum += d
		}
		avgDegree = domain.PyRound(float64(sum)/float64(len(scope)), 3)
	}

	var llmValues []float64
	for _, e := range relevant {
		if e.LLMScoreAToB != nil {
			llmValues = append(llmValues, *e.LLMScoreAToB)
		}
		if e.LLMScoreBToA != nil {
			llmValues = append(llmValues, *e.LLMScoreBToA)
		}
	}
	if len(llmValues) == 0 {
		for _, e := range relevant {
			llmValues = append(llmValues, e.LLMScore)
		}
	}

	withLLM := 0
	withDirectional := 0
	for _, e := range relevant {
		if e.LLMScoreAToB != nil || e.LLMScoreBToA != nil {
			withLLM++
		}
		if e.LLMScoreAToB != nil && e.LLMScoreBToA != nil {
			withDirectional++
		}
	}

	finalWeights := make([]float64, len(relevant))
	embedScores := make([]float64, len(relevant))
	for i, e := range relevant {
		finalWeights[i] = e.FinalWeight
		embedScores[i] = e.EmbedScore
	}

	return map[string]any{
		"overview": map[string]any{
			"total_users":                   len(scope),
			"total_edges":                   totalEdges,
			"average_degree":                avgDegree,
			"edges_with_llm_scores":         withLLM,
			"edges_with_directional_scores": withDirectional,
		},
		"degree_distribution": degreeDist,
		"score_statistics": map[string]any{
			"final_weights":    stats(finalWeights),
			"embedding_scores": stats(embedScores),
			"llm_scores":       stats(llmValues),
		},
		"users": users,
	}
}

// matchEntry 构造单条匹配条目：partner + weight + directional_scores。
func matchEntry(edge domain.Edge, uid domain.UserID) map[string]any {
	partner := edge.User2
	if edge.User1 != uid {
		partner = edge.User1
	}
	entry := map[string]any{
		"partner": string(partner),
		"weight":  domain.PyRound(edge.FinalWeight, 3),
	}
	if edge.LLMScoreAToB != nil || edge.LLMScoreBToA != nil {
		entry["directional_scores"] = map[string]any{
			"a_to_b": roundOptPtr(edge.LLMScoreAToB),
			"b_to_a": roundOptPtr(edge.LLMScoreBToA),
		}
	} else {
		entry["directional_scores"] = nil
	}
	return entry
}

// stats min/max/avg 统计（round 3，与 golden fixture 精度一致）；
// 空列表 → {min: null, max: null, avg: null}。
//
// 注意：avg 的求和是**顺序累加**（与 Python sum() 一致），
// 不用 pairwise——golden 对拍依赖该细节。
func stats(values []float64) map[string]any {
	if len(values) == 0 {
		return map[string]any{"min": nil, "max": nil, "avg": nil}
	}
	lo, hi := values[0], values[0]
	sum := 0.0
	for _, v := range values {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
		sum += v
	}
	return map[string]any{
		"min": domain.PyRound(lo, 3),
		"max": domain.PyRound(hi, 3),
		"avg": domain.PyRound(sum/float64(len(values)), 3),
	}
}

func intKey(d int) string {
	return strconv.Itoa(d)
}

func roundOptPtr(v *float64) any {
	if v == nil {
		return nil
	}
	return domain.PyRound(*v, 3)
}
