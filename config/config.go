// config 包的加载入口：默认配置 → 文件/目录 overlay → 单值 override。
//
// 与 Python 基线 load_config 语义逐条对齐：
//   - configPath 为**文件路径**：整份 YAML 深合并到默认值之上
//     （`evaluate --config custom.yaml` 的自定义门禁由此生效，qodo #4）；
//   - configPath 为**目录路径**：目录下的 YAML 按文件名匹配默认配置的
//     顶层 key（如 blending.yaml 覆盖 blending）；
//   - overrides：点号路径设值（{"blending.embed_weight": 0.4}）。
package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Cloudbird-Software/mutual/internal/engine"
)

//go:embed default.yaml
var defaultYAML []byte

// Config 是加载后的配置（动态 map + 类型化视图）。
//
// 保留动态 map 的原因：overlay/override 是通用深合并操作，天然面向
// 任意 key；类型化视图（Recipe() / Gates() 等）在读取点提供强类型，
// 两者结合 = 灵活加载 + 安全消费。
type Config struct {
	raw map[string]any
	// crossOrder 是 recipe.cross_section_weights 的 YAML 文件序
	// （qodo PR2 #6）：Go map 不保序，而 Python dict 按插入序迭代，
	// 融合是浮点累加、顺序影响末位精度。由 Load 从各配置源捕获。
	crossOrder []string
}

// Default 返回内置默认配置（config/default.yaml）。
func Default() (*Config, error) {
	raw, err := ParseYAML(defaultYAML)
	if err != nil {
		return nil, fmt.Errorf("解析内置 default.yaml: %w", err)
	}
	return &Config{
		raw:        raw,
		crossOrder: KeyOrder(defaultYAML, "recipe", "cross_section_weights"),
	}, nil
}

// Load 加载配置：默认 → 文件/目录 overlay → 单值 override。
//
// 加载即校验（qodo PR2 #5）：结构错误（如 recipe.section_weights
// 被覆盖为标量）在此处报描述性错误，而非读取点 panic。
// configPath 为空串时只应用 overrides。
func Load(configPath string, overrides map[string]any) (*Config, error) {
	cfg, err := Default()
	if err != nil {
		return nil, err
	}
	crossOrder := cfg.crossOrder
	if configPath != "" {
		info, err := os.Stat(configPath)
		if err != nil {
			return nil, fmt.Errorf("配置路径不可访问: %w", err)
		}
		if info.IsDir() {
			// 目录 overlay 中只有 recipe.{yaml,yml} 会触及 recipe 段
			//（其文件体即 recipe 段内容，cross_section_weights 在顶层）。
			for _, name := range []string{"recipe.yaml", "recipe.yml"} {
				if data, err := os.ReadFile(filepath.Join(configPath, name)); err == nil {
					crossOrder = mergeKeyOrder(crossOrder,
						KeyOrder(data, "cross_section_weights"))
					break
				}
			}
			// 显式 mergeErr：内层 := 声明的 err 会遮蔽外层 Stat 的 err，
			// 使外层 if err != nil 恒查 nil、YAML 语法错误被静默吞掉
			//（CodeRabbit）。
			merged, mergeErr := applyDirOverlay(cfg.raw, configPath)
			if mergeErr != nil {
				return nil, mergeErr
			}
			cfg.raw = merged
		} else {
			data, readErr := os.ReadFile(configPath)
			if readErr != nil {
				return nil, fmt.Errorf("配置文件读取失败: %w", readErr)
			}
			crossOrder = mergeKeyOrder(crossOrder,
				KeyOrder(data, "recipe", "cross_section_weights"))
			// 同上：避免 := 遮蔽导致 overlay 错误丢失、raw 被置 nil
			// 后静默退化成空配置（CodeRabbit）。
			merged, mergeErr := applyFileOverlay(cfg.raw, data, configPath)
			if mergeErr != nil {
				return nil, mergeErr
			}
			cfg.raw = merged
		}
	}
	for k, v := range overrides {
		SetDotted(cfg.raw, k, v)
		// override 触及 cross_section_weights 的子键时并入键序
		//（Python dict 赋值语义：已有键原位，新键追加）。
		if strings.HasPrefix(k, "recipe.cross_section_weights.") {
			last := k[strings.LastIndex(k, ".")+1:]
			crossOrder = mergeKeyOrder(crossOrder, []string{last})
		}
	}
	cfg.crossOrder = crossOrder
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// mergeKeyOrder 合并键序：base 序在前，overlay 新键按其文件序追加
// （Python dict.update 语义：已有键保持原位，新键追加）。
func mergeKeyOrder(base, overlay []string) []string {
	seen := make(map[string]bool, len(base)+len(overlay))
	out := make([]string, 0, len(base)+len(overlay))
	for _, k := range base {
		if !seen[k] {
			out = append(out, k)
			seen[k] = true
		}
	}
	for _, k := range overlay {
		if !seen[k] {
			out = append(out, k)
			seen[k] = true
		}
	}
	return out
}

// Raw 返回底层配置 map（只读约定：调用方不得修改）。
func (c *Config) Raw() map[string]any { return c.raw }

func applyFileOverlay(base map[string]any, data []byte, path string) (map[string]any, error) {
	overlay, err := ParseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("配置文件 %s: %w", path, err)
	}
	return DeepMerge(base, overlay), nil
}

func applyDirOverlay(base map[string]any, dir string) (map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("配置目录读取失败: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		key := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		if _, exists := base[key]; !exists {
			continue // 只覆盖默认配置已有的顶层 key（Python 语义）
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		overlay, err := ParseYAML(data)
		if err != nil {
			return nil, fmt.Errorf("配置文件 %s: %w", name, err)
		}
		if sub, ok := mergeEntry(base[key], overlay); ok {
			base[key] = sub
		}
	}
	return base, nil
}

func mergeEntry(baseVal any, overlay map[string]any) (any, bool) {
	if baseMap, ok := baseVal.(map[string]any); ok {
		return DeepMerge(baseMap, overlay), true
	}
	return overlay, true
}

// DeepMerge 递归合并：overlay 的值覆盖 base（不修改入参）。
func DeepMerge(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if bv, ok := out[k]; ok {
			if bm, ok1 := bv.(map[string]any); ok1 {
				if om, ok2 := v.(map[string]any); ok2 {
					out[k] = DeepMerge(bm, om)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}

// SetDotted 按点号路径设值（原地修改）："blending.embed_weight" = 0.4。
func SetDotted(config map[string]any, dottedKey string, value any) {
	keys := strings.Split(dottedKey, ".")
	d := config
	for _, k := range keys[:len(keys)-1] {
		next, ok := d[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			d[k] = next
		}
		d = next
	}
	d[keys[len(keys)-1]] = value
}

// ---------------------------------------------------------------------------
// 类型化视图（读取点强类型；缺省值与 config/default.yaml 对齐）
// ---------------------------------------------------------------------------

// RecipeView 是 recipe 段的类型化视图。
type RecipeView struct {
	Instruction        string
	SectionWeights     map[string]float64
	CrossSectionWeight []engine.WeightEntry
}

// Recipe 返回 recipe 段（相似度融合权重 + 打分指令）。
//
// 防御性读取（qodo PR2 #5）：section_weights / cross_section_weights
// 非 mapping（null/list/标量）时视作缺省，不 panic——结构错误已在
// Load 期由 Validate 拦截，此处兜底直接构造的 Config。
func (c *Config) Recipe() RecipeView {
	recipe := mmap(c.raw["recipe"])
	view := RecipeView{SectionWeights: map[string]float64{}}
	view.Instruction, _ = recipe["instruction"].(string)
	for k, v := range mmap(recipe["section_weights"]) {
		view.SectionWeights[k] = toFloat(v)
	}
	// cross_section_weights 保持 YAML 文件序（qodo PR2 #6）：融合是
	// 浮点累加，term 顺序影响末位精度，必须与 Python 的 dict 插入序
	// 一致。crossOrder 由 Load 从配置源捕获；无序信息时回退字典序
	//（确定性兜底）。
	if cross, ok := recipe["cross_section_weights"].(map[string]any); ok {
		seen := map[string]bool{}
		for _, k := range c.crossOrder {
			if v, ok := cross[k]; ok {
				view.CrossSectionWeight = append(view.CrossSectionWeight,
					engine.WeightEntry{Key: k, Value: toFloat(v)})
				seen[k] = true
			}
		}
		rest := make([]string, 0, len(cross))
		for k := range cross {
			if !seen[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		for _, k := range rest {
			view.CrossSectionWeight = append(view.CrossSectionWeight,
				engine.WeightEntry{Key: k, Value: toFloat(cross[k])})
		}
	}
	return view
}

// Validate 校验合并后配置的结构约束，返回描述性错误（qodo PR2 #5：
// 语法合法但类型错误的 YAML——如 section_weights: null / 列表 /
// 标量——在加载期 fail loud，而非 pipeline 读取点 panic）。
func (c *Config) Validate() error {
	recipe, present := c.raw["recipe"]
	if !present || recipe == nil {
		return nil
	}
	m, ok := recipe.(map[string]any)
	if !ok {
		return fmt.Errorf("配置校验失败: recipe 必须是 mapping，当前为 %s", kindOf(recipe))
	}
	posSum, negSum := 0.0, 0.0
	for _, field := range []string{"section_weights", "cross_section_weights"} {
		v, present := m[field]
		if !present || v == nil {
			continue
		}
		weights, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("配置校验失败: recipe.%s 必须是 mapping，当前为 %s", field, kindOf(v))
		}
		for k, w := range weights {
			if !isNumeric(w) {
				return fmt.Errorf("配置校验失败: recipe.%s.%s 必须是数值，当前为 %s", field, k, kindOf(w))
			}
			f := toFloat(w)
			if f > 0 {
				posSum += f
			} else if f < 0 {
				negSum += f
			}
		}
	}
	// 负权重规则（CodeRabbit）：引擎按 Python 基线语义对 |denom|>eps 的
	// cell 一律除法（含负分母——单负权重 section 是刻意的惩罚项设计，
	// 如默认 recipe 的 skills/-0.10）。配置层保证两条必要条件：
	// 全量 mask 分母（权重总和）为正，且正权重足以压过负权重总量——
	// 否则自定义权重可构造出极小/负的有效分母，相似度被异常放大或翻转。
	// 未配置任何权重（空 recipe）不做该校验（相似度退化为零矩阵是显式行为）。
	total := posSum + negSum
	if posSum == 0 && negSum == 0 {
		return nil
	}
	if total <= 0 {
		return fmt.Errorf(
			"配置校验失败: recipe 权重总和必须为正（全量 mask 分母），当前 %.4g", total)
	}
	if -negSum >= posSum {
		return fmt.Errorf(
			"配置校验失败: recipe 负权重总量（%.4g）必须小于正权重总量（%.4g），"+
				"否则有效分母可为极小/负值，相似度被异常放大或符号翻转", -negSum, posSum)
	}
	return nil
}

// kindOf 返回配置值的类型描述（校验错误信息用）。
func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case []any:
		return "list"
	case string:
		return "string"
	case bool:
		return "bool"
	case int, float64:
		return "number"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// isNumeric 判定配置值是否为数值（int/float；字符串数字不算——
// 权重写成带引号的字符串是配置错误，不应静默转换）。
func isNumeric(v any) bool {
	switch v.(type) {
	case int, float64:
		return true
	}
	return false
}

// RecipeConfig 直接返回 engine 的相似度配置（Instruction 独立于
// 相似度融合权重，由调用方单独取用）。
func (c *Config) RecipeConfig() engine.RecipeConfig {
	r := c.Recipe()
	return engine.RecipeConfig{
		SectionWeights:      r.SectionWeights,
		CrossSectionWeights: r.CrossSectionWeight,
	}
}

// Blending 返回 embed/llm 分数混合权重。
func (c *Config) Blending() engine.BlendingConfig {
	b := mmap(c.raw["blending"])
	return engine.BlendingConfig{
		EmbedWeight: toFloatDefault(b["embed_weight"], 0.35),
		LLMWeight:   toFloatDefault(b["llm_weight"], 0.65),
	}
}

// FeedbackCalibration 返回 calibrate 子命令的参数（CodeRabbit：校准
// 参数不硬编码——起点 blending 取 Blending()，此处只补模板与窗口）。
type FeedbackCalibration struct {
	// PromptBase 是 prompt 校准块的基础模板。
	PromptBase string
	// Window 是校准块取最近几条评测历史。
	Window int
}

// Calibration 返回 feedback 段的校准参数。
func (c *Config) Calibration() FeedbackCalibration {
	f := mmap(c.raw["feedback"])
	base, _ := f["calibration_prompt_base"].(string)
	if base == "" {
		base = "Score this match..."
	}
	return FeedbackCalibration{
		PromptBase: base,
		Window:     toIntDefault(f["calibration_window"], 3),
	}
}

// BudgetsView 是 LLM 打分预算。
type BudgetsView struct {
	PerProfileCap int // 每用户最多打分对数
	MaxCalls      int // 全局调用上限
	BatchSize     int // 一次 prompt 打几对
}

// Budgets 返回 budgets 段。
func (c *Config) Budgets() BudgetsView {
	b := mmap(c.raw["budgets"])
	return BudgetsView{
		PerProfileCap: toIntDefault(b["max_n_llm_evaluations_per_profile"], 24),
		MaxCalls:      toIntDefault(b["max_pair_llm_calls"], 1200),
		BatchSize:     toIntDefault(b["n_profiles_to_score_together"], 2),
	}
}

// Matching 返回度约束段（pool_b_max 为 null → nil = 不限）。
func (c *Config) Matching() engine.MatchingConfig {
	m := mmap(c.raw["matching"])
	var poolBMax *int
	if v, ok := m["pool_b_max"]; ok && v != nil {
		n := toIntDefault(v, 0)
		poolBMax = &n
	}
	return engine.MatchingConfig{
		BMin:     toIntDefault(m["b_min"], 0),
		BMax:     toIntDefault(m["b_max"], 0),
		PoolBMax: poolBMax,
	}
}

// GatesView 返回评测门禁段。
type GatesView struct {
	HRAt3Min     float64
	NDCGAt5Min   float64
	TotalEnvyMax int
}

// Gates 返回 evaluation.gates 段。
func (c *Config) Gates() GatesView {
	ev := mmap(c.raw["evaluation"])
	g := mmap(ev["gates"])
	return GatesView{
		HRAt3Min:     toFloatDefault(g["hr_at_3_min"], 0.6),
		NDCGAt5Min:   toFloatDefault(g["ndcg_at_5_min"], 0.4),
		TotalEnvyMax: toIntDefault(g["total_envy_max"], 2),
	}
}

// ModelsView 是模型配置段。
type ModelsView struct {
	Embedding           string
	EmbeddingDim        int // 0 = 全尺寸（null）
	EmbeddingBaseURL    string
	PairLLM             string
	ReasoningEffort     string
	PairReasoningEffort string
	BaseURL             string
}

// Models 返回 models 段。
func (c *Config) Models() ModelsView {
	m := mmap(c.raw["models"])
	dim := 0
	if v, ok := m["embedding_dimensions"]; ok && v != nil {
		dim = toIntDefault(v, 0)
	}
	return ModelsView{
		Embedding:           str(m["embedding"]),
		EmbeddingDim:        dim,
		EmbeddingBaseURL:    str(m["embedding_base_url"]),
		PairLLM:             str(m["pair_llm"]),
		ReasoningEffort:     str(m["reasoning_effort"]),
		PairReasoningEffort: str(m["pair_reasoning_effort"]),
		BaseURL:             str(m["base_url"]),
	}
}

// HydeNDescriptors 返回 hyde.n_descriptors。
func (c *Config) HydeNDescriptors() int {
	return toIntDefault(mmap(mmap(c.raw["hyde"]))["n_descriptors"], 1)
}

// MatchingMinProfiles 返回 matching.min_profiles_required（运行守卫）。
func (c *Config) MatchingMinProfiles() int {
	return toIntDefault(mmap(c.raw["matching"])["min_profiles_required"], 0)
}

// MatchingHardFilter 返回 matching.hard_constraint_filter——硬约束资格
// 过滤（EligibilityExclusions 前置于候选选择，违反对不消耗 LLM 预算）。
// 生产姿态默认开启；语料无显式约束标记时零行为差异（fail-safe：
// 只认 "hard constraint/硬约束" 显式声明 + counterpart 可见违反自述）。
func (c *Config) MatchingHardFilter() bool {
	v := mmap(c.raw["matching"])["hard_constraint_filter"]
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

// NoveltyWindowMonths 返回 matching.novelty_window_months。
func (c *Config) NoveltyWindowMonths() int {
	return toIntDefault(mmap(c.raw["matching"])["novelty_window_months"], 6)
}

// TopMatchesPerUser 返回 reporting.top_matches_per_user（0 = 不截断）。
func (c *Config) TopMatchesPerUser() int {
	return toIntDefault(mmap(c.raw["reporting"])["top_matches_per_user"], 0)
}

// NValues 返回 evaluation.n_values（HR@K 的 K 列表）。
func (c *Config) NValues() []int {
	ev := mmap(c.raw["evaluation"])
	raw, _ := ev["n_values"].([]any)
	out := make([]int, 0, len(raw))
	for _, v := range raw {
		out = append(out, toIntDefault(v, 0))
	}
	return out
}

func mmap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func toFloat(v any) float64 { return toFloatDefault(v, 0) }

func toFloatDefault(v any, def float64) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return f
		}
	}
	return def
}

func toIntDefault(v any, def int) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}
