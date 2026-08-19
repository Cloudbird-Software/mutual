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
}

// Default 返回内置默认配置（config/default.yaml）。
func Default() (*Config, error) {
	raw, err := ParseYAML(defaultYAML)
	if err != nil {
		return nil, fmt.Errorf("解析内置 default.yaml: %w", err)
	}
	return &Config{raw: raw}, nil
}

// Load 加载配置：默认 → 文件/目录 overlay → 单值 override。
//
// configPath 为空串时只应用 overrides。
func Load(configPath string, overrides map[string]any) (*Config, error) {
	cfg, err := Default()
	if err != nil {
		return nil, err
	}
	if configPath != "" {
		info, err := os.Stat(configPath)
		if err != nil {
			return nil, fmt.Errorf("配置路径不可访问: %w", err)
		}
		if info.IsDir() {
			cfg.raw, err = applyDirOverlay(cfg.raw, configPath)
		} else {
			cfg.raw, err = applyFileOverlay(cfg.raw, configPath)
		}
		if err != nil {
			return nil, err
		}
	}
	for k, v := range overrides {
		SetDotted(cfg.raw, k, v)
	}
	return cfg, nil
}

// Raw 返回底层配置 map（只读约定：调用方不得修改）。
func (c *Config) Raw() map[string]any { return c.raw }

func applyFileOverlay(base map[string]any, path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("配置文件读取失败: %w", err)
	}
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
func (c *Config) Recipe() RecipeView {
	recipe, _ := c.raw["recipe"].(map[string]any)
	view := RecipeView{SectionWeights: map[string]float64{}}
	if recipe == nil {
		return view
	}
	view.Instruction, _ = recipe["instruction"].(string)
	for k, v := range recipe["section_weights"].(map[string]any) {
		view.SectionWeights[k] = toFloat(v)
	}
	// cross_section_weights 保持文件序（WeightEntry 保序，engine 依赖）。
	if cross, ok := recipe["cross_section_weights"].(map[string]any); ok {
		keys := make([]string, 0, len(cross))
		for k := range cross {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			view.CrossSectionWeight = append(view.CrossSectionWeight,
				engine.WeightEntry{Key: k, Value: toFloat(cross[k])})
		}
	}
	return view
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
	b, _ := c.raw["blending"].(map[string]any)
	return engine.BlendingConfig{
		EmbedWeight: toFloatDefault(mmap(b)["embed_weight"], 0.35),
		LLMWeight:   toFloatDefault(mmap(b)["llm_weight"], 0.65),
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
