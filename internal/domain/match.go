package domain

// Edge 是最终匹配边（match 阶段输出，introduce 阶段补话术）。
type Edge struct {
	User1                UserID // 字典序较小者
	User2                UserID // 字典序较大者
	PairID               PairID
	FinalWeight          float64 // 融合权重（embed_weight/llm_weight 混合）
	EmbedScore           float64
	LLMScore             float64
	EmbedScoreNormalized *float64
	LLMScoreNormalized   *float64
	LLMScoreAToB         *float64
	LLMScoreBToA         *float64
	Intro                string
	StarterTopics        string
}

// ToMap 与 Python Edge.to_dict 逐字段一致（round 3 位；nil → null，
// 键恒存在——与 Python 不同，Edge 的可空字段不做键省略）。
func (e Edge) ToMap() map[string]any {
	return map[string]any{
		"user1":                  string(e.User1),
		"user2":                  string(e.User2),
		"pair_id":                string(e.PairID),
		"final_weight":           PyRound(e.FinalWeight, 3),
		"embed_score":            PyRound(e.EmbedScore, 3),
		"llm_score":              PyRound(e.LLMScore, 3),
		"embed_score_normalized": roundOpt(e.EmbedScoreNormalized, 3),
		"llm_score_normalized":   roundOpt(e.LLMScoreNormalized, 3),
		"llm_score_a_to_b":       roundOpt(e.LLMScoreAToB, 3),
		"llm_score_b_to_a":       roundOpt(e.LLMScoreBToA, 3),
		"intro":                  e.Intro,
		"starter_topics":         e.StarterTopics,
	}
}

// MatchResult 是一次匹配运行的完整输出（runners 的聚合产物）。
type MatchResult struct {
	Edges      []Edge
	ReportData map[string]any
	NewPairs   []map[string]any // match_history 追加行 {pair_id, user1, user2, matched_at}
	EnvyReport map[string]any   // 可选；nil → JSON null
}

// ToMap 与 Python MatchResult.to_dict 逐字段一致。
func (mr MatchResult) ToMap() map[string]any {
	edges := make([]any, len(mr.Edges))
	for i, e := range mr.Edges {
		edges[i] = e.ToMap()
	}
	newPairs := make([]any, len(mr.NewPairs))
	for i, p := range mr.NewPairs {
		newPairs[i] = p
	}
	var envy any
	if mr.EnvyReport != nil {
		envy = mr.EnvyReport
	}
	return map[string]any{
		"edges":       edges,
		"report_data": mr.ReportData,
		"new_pairs":   newPairs,
		"envy_report": envy,
	}
}
