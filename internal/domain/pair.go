package domain

// CandidatePair 是进入 LLM 精排的候选对（select 阶段输出）。
//
// 构造即规范：NewCandidatePair 强制 user1 ≤ user2（字典序）并派生
// PairID，调用方无法构造出顺序未归一化的候选对。
type CandidatePair struct {
	User1           UserID // 字典序较小者（构造函数保证）
	User2           UserID // 字典序较大者（构造函数保证）
	PairID          PairID
	SimilarityScore float64
}

// NewCandidatePair 构造归一化的候选对。
func NewCandidatePair(a, b UserID, score float64) CandidatePair {
	pair := CandidatePair{
		User1:           a,
		User2:           b,
		PairID:          StablePairID(a, b),
		SimilarityScore: score,
	}
	if b < a {
		pair.User1, pair.User2 = b, a
	}
	return pair
}

// PairScore 是 LLM 精排后的双向打分结果（score 阶段输出）。
//
// 方向性分数：LLMScoreAToB 是 A 对 B 的评价，LLMScoreBToA 反之；
// 两者不可互换（spec 的方向性互惠核心）。nil 表示未打分——
// 未打分候选保留 embedding 权重，不丢弃（spec/05-boundaries.md §3）。
type PairScore struct {
	PairID               PairID
	User1                UserID
	User2                UserID
	EmbedScore           float64
	LLMScore             *float64
	LLMScoreAToB         *float64
	LLMScoreBToA         *float64
	EmbedScoreNormalized *float64
	LLMScoreNormalized   *float64
}

// NewPairScore 构造 PairScore，并像 Python 基线一样对 pair 归一化
// （user1 ≤ user2，pair_id 与顺序无关）。
func NewPairScore(a, b UserID, embedScore float64) PairScore {
	ps := PairScore{
		PairID:     StablePairID(a, b),
		User1:      a,
		User2:      b,
		EmbedScore: embedScore,
	}
	if b < a {
		ps.User1, ps.User2 = b, a
	}
	return ps
}

// ToMap 与 Python PairScore.to_dict 逐字段一致（round 到 3 位小数，
// banker's rounding）。方向性 llm 分数字段恒存在（nil → null）；
// normalized 字段为 nil 时**省略键**——与 Python to_dict 的条件赋值
// 对齐，golden 差分测试按键集合严格比较。
func (ps PairScore) ToMap() map[string]any {
	d := map[string]any{
		"pair_id":          string(ps.PairID),
		"user1":            string(ps.User1),
		"user2":            string(ps.User2),
		"embed_score":      PyRound(ps.EmbedScore, 3),
		"llm_score":        roundOpt(ps.LLMScore, 3),
		"llm_score_a_to_b": roundOpt(ps.LLMScoreAToB, 3),
		"llm_score_b_to_a": roundOpt(ps.LLMScoreBToA, 3),
	}
	if ps.EmbedScoreNormalized != nil {
		d["embed_score_normalized"] = PyRound(*ps.EmbedScoreNormalized, 3)
	}
	if ps.LLMScoreNormalized != nil {
		d["llm_score_normalized"] = PyRound(*ps.LLMScoreNormalized, 3)
	}
	return d
}

// Introduction 是双向对接话术 + 破冰话题（introduce 阶段输出）。
// LLM 失败时由模板兜底话术填充（spec/05-boundaries.md §9 的降级路径）。
type Introduction struct {
	PairID        PairID
	Intro         string
	StarterTopics string
}

// ToMap 与 Python Introduction 的序列化形态一致。
func (i Introduction) ToMap() map[string]any {
	return map[string]any{
		"pair_id":        string(i.PairID),
		"intro":          i.Intro,
		"starter_topics": i.StarterTopics,
	}
}

func roundOpt(v *float64, n int) any {
	if v == nil {
		return nil
	}
	return PyRound(*v, n)
}
