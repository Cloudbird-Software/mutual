package domain

// Gates 是评测门禁（spec/03-oracles.md）：CI 用最小质量线。
// 零值无效——用 DefaultGates() 或配置构造。
type Gates struct {
	HRAt3Min     float64 // HR@3 下界（默认 0.6）
	NDCGAt5Min   float64 // NDCG@5 下界（默认 0.4）
	TotalEnvyMax int     // envy 总数上界（默认 2）
}

// DefaultGates 返回 spec/03-oracles.md 的默认门禁。
func DefaultGates() Gates {
	return Gates{HRAt3Min: 0.6, NDCGAt5Min: 0.4, TotalEnvyMax: 2}
}

// GatesFromMap 从配置 dict（config["evaluation"]["gates"]）构造；
// 缺省键回落默认值（与 Python gates.get(k, default) 语义一致）。
func GatesFromMap(m map[string]any) Gates {
	g := DefaultGates()
	if v, ok := asFloat(m["hr_at_3_min"]); ok {
		g.HRAt3Min = v
	}
	if v, ok := asFloat(m["ndcg_at_5_min"]); ok {
		g.NDCGAt5Min = v
	}
	if v, ok := m["total_envy_max"]; ok {
		switch n := v.(type) {
		case int:
			g.TotalEnvyMax = n
		case int64:
			g.TotalEnvyMax = int(n)
		case float64:
			g.TotalEnvyMax = int(n)
		}
	}
	return g
}

// EvaluationReport 是评测结果（evaluate 阶段输出），作为 LLM 自我
// 改进的反馈（spec/03-oracles.md）。HR@K / NDCG@5 度量推荐质量，
// envy 计数度量互惠公平。
type EvaluationReport struct {
	HRAt1          float64
	HRAt3          float64
	HRAt5          float64
	NDCGAt5        float64
	EnvyCountLeft  int
	EnvyCountRight int
	TotalScenarios int
	Metadata       map[string]any
}

// TotalEnvy 返回两侧 envy 之和。
func (r EvaluationReport) TotalEnvy() int {
	return r.EnvyCountLeft + r.EnvyCountRight
}

// PassesGates 检查是否通过 CI 门禁。
// gates 为 nil 时用 DefaultGates（与 Python gates.get(k, d) 一致）。
func (r EvaluationReport) PassesGates(gates *Gates) bool {
	g := DefaultGates()
	if gates != nil {
		g = *gates
	}
	return r.HRAt3 >= g.HRAt3Min &&
		r.NDCGAt5 >= g.NDCGAt5Min &&
		r.TotalEnvy() <= g.TotalEnvyMax
}

// ToMap 与 Python EvaluationReport.to_dict 逐字段一致
// （指标 round 4 位；metadata 直通）。
func (r EvaluationReport) ToMap() map[string]any {
	meta := r.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	return map[string]any{
		"hr_at_1":          PyRound(r.HRAt1, 4),
		"hr_at_3":          PyRound(r.HRAt3, 4),
		"hr_at_5":          PyRound(r.HRAt5, 4),
		"ndcg_at_5":        PyRound(r.NDCGAt5, 4),
		"envy_count_left":  r.EnvyCountLeft,
		"envy_count_right": r.EnvyCountRight,
		"total_scenarios":  r.TotalScenarios,
		"metadata":         meta,
	}
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
