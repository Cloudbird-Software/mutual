package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultValues 内置默认配置的类型化视图与 config/default.yaml 一致。
func TestDefaultValues(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if b := cfg.Blending(); b.EmbedWeight != 0.35 || b.LLMWeight != 0.65 {
		t.Errorf("blending: got %+v", b)
	}
	m := cfg.Matching()
	if m.BMin != 3 || m.BMax != 4 || m.PoolBMax != nil {
		t.Errorf("matching: got %+v", m)
	}
	if cfg.MatchingMinProfiles() != 2 {
		t.Errorf("min_profiles_required: got %d want 2", cfg.MatchingMinProfiles())
	}
	if !cfg.MatchingHardFilter() {
		t.Errorf("matching.hard_constraint_filter: got false want true（生产姿态默认开启）")
	}
	if cfg.NoveltyWindowMonths() != 6 {
		t.Errorf("novelty_window_months: got %d want 6", cfg.NoveltyWindowMonths())
	}
	bud := cfg.Budgets()
	if bud.PerProfileCap != 48 || bud.MaxCalls != 4800 || bud.BatchSize != 2 {
		t.Errorf("budgets: got %+v", bud)
	}
	g := cfg.Gates()
	if g.HRAt3Min != 0.6 || g.NDCGAt5Min != 0.4 || g.TotalEnvyMax != 2 {
		t.Errorf("gates: got %+v", g)
	}
	if cfg.HydeNDescriptors() != 1 {
		t.Errorf("hyde.n_descriptors: got %d want 1", cfg.HydeNDescriptors())
	}
	nv := cfg.NValues()
	if len(nv) != 3 || nv[0] != 1 || nv[1] != 3 || nv[2] != 5 {
		t.Errorf("n_values: got %v", nv)
	}
	// 报告条数未配置（S6）→ 0 = 不截断。
	if cfg.TopMatchesPerUser() != 0 {
		t.Errorf("top_matches_per_user: got %d want 0", cfg.TopMatchesPerUser())
	}
}

// TestLoadDottedOverrides 点号路径 override（golden 测试用它解耦 batch 预算）。
func TestLoadDottedOverrides(t *testing.T) {
	cfg, err := Load("", map[string]any{
		"budgets.n_profiles_to_score_together": 1,
		"blending.embed_weight":                0.4,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Budgets().BatchSize != 1 {
		t.Errorf("batch size: got %d want 1", cfg.Budgets().BatchSize)
	}
	if cfg.Blending().EmbedWeight != 0.4 {
		t.Errorf("embed_weight: got %v want 0.4", cfg.Blending().EmbedWeight)
	}
}

// TestLoadFileOverlay 整份 YAML 深合并（evaluate --config custom.yaml
// 的自定义门禁由此生效，qodo #4）。
func TestLoadFileOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	yaml := `# 自定义门禁（注释行应被剥离）
evaluation:
  gates:
    hr_at_3_min: 0.9
    ndcg_at_5_min: 0.7
blending:
  embed_weight: 0.5   # 行内注释
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("写入临时配置: %v", err)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := cfg.Gates()
	if g.HRAt3Min != 0.9 || g.NDCGAt5Min != 0.7 {
		t.Errorf("自定义门禁未生效: got %+v", g)
	}
	// 未覆盖的键保持默认（深合并不是整段替换）。
	if g.TotalEnvyMax != 2 {
		t.Errorf("total_envy_max 应保持默认 2: got %d", g.TotalEnvyMax)
	}
	if cfg.Blending().EmbedWeight != 0.5 {
		t.Errorf("embed_weight: got %v want 0.5", cfg.Blending().EmbedWeight)
	}
	if cfg.Blending().LLMWeight != 0.65 {
		t.Errorf("llm_weight 应保持默认: got %v", cfg.Blending().LLMWeight)
	}
}

// TestLoadDirOverlay 目录 overlay：文件名匹配默认配置的顶层 key。
func TestLoadDirOverlay(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blending.yaml"),
		[]byte("embed_weight: 0.2\nllm_weight: 0.8\n"), 0o644); err != nil {
		t.Fatalf("写入 blending.yaml: %v", err)
	}
	// 未知顶层 key 的文件被忽略（Python 语义）。
	if err := os.WriteFile(filepath.Join(dir, "unknown.yaml"),
		[]byte("foo: 1\n"), 0o644); err != nil {
		t.Fatalf("写入 unknown.yaml: %v", err)
	}
	cfg, err := Load(dir, nil)
	if err != nil {
		t.Fatalf("Load(dir): %v", err)
	}
	if cfg.Blending().EmbedWeight != 0.2 || cfg.Blending().LLMWeight != 0.8 {
		t.Errorf("目录 overlay 未生效: got %+v", cfg.Blending())
	}
}

// TestLoadMissingPath 配置路径不可访问 → fail loud。
func TestLoadMissingPath(t *testing.T) {
	if _, err := Load("/nonexistent/config.yaml", nil); err == nil {
		t.Fatal("不存在的路径应报错")
	}
}

// TestLoadRejectsSyntacticallyInvalidYAML 语法非法的 YAML（tab 缩进）
// → 加载期报错，而非被 err 遮蔽吞掉后静默退化成空配置（CodeRabbit）。
func TestLoadRejectsSyntacticallyInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(path, []byte("recipe:\n\tsection_weights:\n"), 0o644); err != nil {
		t.Fatalf("写入临时配置: %v", err)
	}
	_, err := Load(path, nil)
	if err == nil {
		t.Fatal("语法非法的 YAML 应在 Load 期报错（曾因 err 遮蔽被静默吞掉）")
	}
}

// TestLoadRejectsMalformedRecipe 语法合法但类型错误的 recipe
// （section_weights 被 overlay 替换为列表 / 标量）→ 加载期描述性
// 错误，而非 pipeline 读取点 panic（qodo PR2 #5）。
// null 不在内：Python 语义把它当缺省（or {}），此处一致处理。
func TestLoadRejectsMalformedRecipe(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"list", "recipe:\n  section_weights: [1, 2]\n"},
		{"scalar", "recipe:\n  section_weights: 0.5\n"},
		{"cross_scalar", "recipe:\n  cross_section_weights: 0.5\n"},
		{"weight_string", "recipe:\n  section_weights:\n    skills: \"0.3\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "custom.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
				t.Fatalf("写入临时配置: %v", err)
			}
			_, err := Load(path, nil)
			if err == nil {
				t.Fatalf("类型错误的 recipe 应在 Load 期报错（%s）", tc.yaml)
			}
			if !strings.Contains(err.Error(), "recipe") {
				t.Errorf("错误应指向 recipe 段: %v", err)
			}
		})
	}
}

// TestLoadNullRecipeSectionIsNullSemanticallyNull section_weights: null
// 按缺省处理（Python `or {}` 语义），不报错、不 panic。
func TestLoadNullRecipeSectionIsNullSemanticallyNull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	if err := os.WriteFile(path, []byte("recipe:\n  section_weights:\n"), 0o644); err != nil {
		t.Fatalf("写入临时配置: %v", err)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("null 应视作缺省（Python 语义）: %v", err)
	}
	if len(cfg.Recipe().SectionWeights) != 0 {
		t.Errorf("null section_weights → 空权重: got %v", cfg.Recipe().SectionWeights)
	}
}

// TestLoadOverrideRejectsScalarRecipe override 把 section_weights
// 整体替换为标量 → 同样在加载期拦截。
func TestLoadOverrideRejectsScalarRecipe(t *testing.T) {
	_, err := Load("", map[string]any{"recipe.section_weights": 0.4})
	if err == nil || !strings.Contains(err.Error(), "section_weights") {
		t.Fatalf("标量 override 应报错: got %v", err)
	}
}

// TestRecipeDirectConstructNoPanic 直接构造的 Config（无 crossOrder）
// 读取畸形 recipe 不 panic（防御性兜底）。
func TestRecipeDirectConstructNoPanic(t *testing.T) {
	cfg := &Config{raw: map[string]any{
		"recipe": map[string]any{"section_weights": []any{1, 2}},
	}}
	view := cfg.Recipe() // 不 panic 即通过
	if len(view.SectionWeights) != 0 {
		t.Errorf("畸形 section_weights 应视作缺省: got %v", view.SectionWeights)
	}
}

// TestCrossSectionWeightPreservesYAMLOrder cross_section_weights 按
// YAML 文件序输出（qodo PR2 #6）：融合是浮点累加，term 顺序影响
// 末位精度，必须与 Python dict 插入序一致——字典序输出是 bug。
// 合并序 = Python dict.update 语义：默认配置已有键原位，overlay
// 新键按其文件序追加。
func TestCrossSectionWeightPreservesYAMLOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yaml")
	// overlay 文件序刻意取非字典序（vision_needs 在 project_vision 前）
	// 暴露排序 bug；默认配置的 needs_skills 在最前（dict.update 语义）。
	yaml := `recipe:
  section_weights:
    skills: 0.3
  cross_section_weights:
    vision_needs: 0.1
    project_vision: 0.25
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("写入临时配置: %v", err)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Recipe().CrossSectionWeight
	want := []string{"needs_skills", "vision_needs", "project_vision"}
	if len(got) != len(want) {
		t.Fatalf("cross 项数: got %d want %d（%+v）", len(got), len(want), got)
	}
	for i, k := range want {
		if got[i].Key != k {
			t.Errorf("cross[%d]: got %s want %s（文件序）", i, got[i].Key, k)
		}
	}
}

// TestCrossSectionWeightOverrideAppendsOrder override 追加的新键排在
// 文件序之后（Python dict 赋值语义：已有键原位，新键追加）。
func TestCrossSectionWeightOverrideAppendsOrder(t *testing.T) {
	cfg, err := Load("", map[string]any{
		"recipe.cross_section_weights.needs_skills": 0.45,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Recipe().CrossSectionWeight
	if len(got) != 1 || got[0].Key != "needs_skills" || got[0].Value != 0.45 {
		t.Fatalf("override 后: got %+v", got)
	}
}

// TestKeyOrder KeyOrder 捕获嵌套 mapping 的文件序；块标量体不干扰。
func TestKeyOrder(t *testing.T) {
	data := []byte(`recipe:
  instruction: >
    folded text mentioning cross_section_weights
    and fake keys like needs_skills
  cross_section_weights:
    zeta_first: 1
    alpha_second: 2
`)
	got := KeyOrder(data, "recipe", "cross_section_weights")
	if len(got) != 2 || got[0] != "zeta_first" || got[1] != "alpha_second" {
		t.Errorf("KeyOrder: got %v want [zeta_first alpha_second]", got)
	}
	if got := KeyOrder(data, "recipe"); len(got) != 2 || got[0] != "instruction" {
		t.Errorf("KeyOrder(recipe): got %v", got)
	}
	if got := KeyOrder(data, "nonexistent"); got != nil {
		t.Errorf("不存在的 path: got %v want nil", got)
	}
	if got := KeyOrder(data, "recipe", "instruction"); got != nil {
		t.Errorf("块标量非 mapping: got %v want nil", got)
	}
}

// TestParseYAMLComments 注释剥离：行首注释、行内注释、引号内 # 不剥离。
func TestParseYAMLComments(t *testing.T) {
	doc, err := ParseYAML([]byte("# 头部注释\nkey: value # 尾注\nurl: \"http://x/#frag\"\nlist: [a, b] # 行内列表注释\n"))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if doc["key"] != "value" {
		t.Errorf("key: got %v", doc["key"])
	}
	if doc["url"] != "http://x/#frag" {
		t.Errorf("引号内 # 不应剥离: got %v", doc["url"])
	}
	list, ok := doc["list"].([]any)
	if !ok || len(list) != 2 || list[0] != "a" || list[1] != "b" {
		t.Errorf("list: got %v", doc["list"])
	}
}

// TestDeepMerge 深合并不修改入参、嵌套 dict 递归合并。
func TestDeepMerge(t *testing.T) {
	base := map[string]any{
		"a": map[string]any{"x": 1, "y": 2},
		"b": 1,
	}
	overlay := map[string]any{
		"a": map[string]any{"y": 3, "z": 4},
		"c": 5,
	}
	out := DeepMerge(base, overlay)
	am := out["a"].(map[string]any)
	if am["x"] != 1 || am["y"] != 3 || am["z"] != 4 {
		t.Errorf("嵌套合并: got %v", am)
	}
	if out["b"] != 1 || out["c"] != 5 {
		t.Errorf("顶层合并: got %v", out)
	}
	// 入参不被修改。
	if base["a"].(map[string]any)["y"] != 2 {
		t.Error("DeepMerge 修改了入参 base")
	}
}

// TestSetDotted 点号路径设值：跨层创建中间 dict。
func TestSetDotted(t *testing.T) {
	m := map[string]any{}
	SetDotted(m, "matching.b_min", 2)
	sub := m["matching"].(map[string]any)
	if sub["b_min"] != 2 {
		t.Errorf("SetDotted: got %v", sub)
	}
}

// TestRecipeView recipe 段（相似度权重 + 指令）。
func TestRecipeView(t *testing.T) {
	cfg, _ := Default()
	r := cfg.Recipe()
	if r.Instruction == "" {
		t.Error("recipe.instruction 应非空")
	}
	if r.SectionWeights["vision"] != 0.35 {
		t.Errorf("section_weights.vision: got %v want 0.35", r.SectionWeights["vision"])
	}
	if len(r.CrossSectionWeight) != 1 || r.CrossSectionWeight[0].Key != "needs_skills" ||
		r.CrossSectionWeight[0].Value != 0.80 {
		t.Errorf("cross_section_weights: got %+v", r.CrossSectionWeight)
	}
}

// TestModelsView models 段（embedding_dimensions: null → 0 = 全尺寸）。
func TestModelsView(t *testing.T) {
	cfg, _ := Default()
	m := cfg.Models()
	if m.Embedding != "voyage-3-lite" || m.PairLLM != "LongCat-2.0" {
		t.Errorf("models: got %+v", m)
	}
	if m.EmbeddingDim != 0 {
		t.Errorf("embedding_dimensions null → 0（全尺寸）: got %d", m.EmbeddingDim)
	}
}
