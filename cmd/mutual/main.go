// mutual 是引擎的 CLI 入口（对应 Python python -m mutual.cli）。
//
// 用法：
//
//	mutual evaluate [--config PATH] [--seed S] [--noise-scale F]
//	                [--fail-on-gate] [--json]
//	mutual calibrate --history reports.json [--embedding-only]
//
// 设计原则：全部离线、确定性，不调用真实 LLM，CI 无需 API 凭据即可运行。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Cloudbird-Software/mutual/config"
	"github.com/Cloudbird-Software/mutual/internal/bench"
	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
	"github.com/Cloudbird-Software/mutual/internal/feedback"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "evaluate":
		code = cmdEvaluate(os.Args[2:])
	case "calibrate":
		code = cmdCalibrate(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", os.Args[1])
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprint(os.Stderr, `Mutual 双向互惠推荐引擎 — CLI

用法:
  mutual evaluate   运行离线评测套件并判定门禁
  mutual calibrate  按评测历史做权重/prompt 校准（反馈注入）

evaluate 选项:
  --config PATH        配置文件路径（门禁数值来源，默认 config/default.yaml）
  --seed S             合成市场/bench 随机种子（默认 0）
  --noise-scale F      surrogate 噪声幅度（默认 0.24）
  --fail-on-gate       门禁未达标时非零退出（CI 阻断）
  --json               以 JSON 输出评测报告
  --extended           附带运行扩展陷阱套件（诊断输出，不计入门禁；
                       生产姿态：blending 取配置值 + 保底推荐 + 资格过滤）

calibrate 选项:
  --config PATH        配置文件路径（校准起点 blending 与参数来源，默认 config/default.yaml）
  --history PATH       评测历史 JSON 文件（list of EvaluationReport.to_dict()，时间升序）
  --embedding-only     只输出 prompt 校准块，不调权重
`)
}

// cmdEvaluate 运行离线评测套件：三场景 bench（classic/drift/cold）
// + market 合成市场（构造性 oracle）。
func cmdEvaluate(args []string) int {
	fs := flag.NewFlagSet("evaluate", flag.ExitOnError)
	configPath := fs.String("config", "config/default.yaml", "配置文件路径")
	seed := fs.Int("seed", 0, "随机种子")
	noiseScale := fs.Float64("noise-scale", 0.24, "surrogate 噪声幅度")
	failOnGate := fs.Bool("fail-on-gate", false, "门禁未达标时非零退出")
	asJSON := fs.Bool("json", false, "以 JSON 输出评测报告")
	extended := fs.Bool("extended", false, "附带运行扩展陷阱套件（诊断输出）")
	_ = fs.Parse(args)

	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置加载失败: %v\n", err)
		return 2
	}

	reports, err := bench.RunSuite(*seed, *noiseScale)
	if err != nil {
		fmt.Fprintf(os.Stderr, "评测套件运行失败: %v\n", err)
		return 2
	}

	// 输入完整性（CodeRabbit）：map 读缺失 key 得零值报告——envy=0 会让
	// 门禁在数据缺失时反而变宽松。CI 门禁必须在输入不完整时失败。
	scenarioReports := make([]domain.EvaluationReport, 0, len(bench.ScenarioNames))
	for _, name := range bench.ScenarioNames {
		r, ok := reports[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "评测套件缺少场景 %q（输入不完整，拒绝判定门禁）\n", name)
			return 2
		}
		scenarioReports = append(scenarioReports, r)
	}
	marketReport, ok := reports["market"]
	if !ok {
		fmt.Fprintln(os.Stderr, "评测套件缺少 market 场景（输入不完整，拒绝判定门禁）")
		return 2
	}
	qualityAgg := bench.AggregateReports(scenarioReports, bench.ScenarioNames)
	// envy 门禁覆盖全部信号源（三场景 + market 构造性 oracle）。
	totalEnvy := qualityAgg.TotalEnvy() + marketReport.TotalEnvy()
	gateReport := domain.EvaluationReport{
		HRAt1:          qualityAgg.HRAt1,
		HRAt3:          qualityAgg.HRAt3,
		HRAt5:          qualityAgg.HRAt5,
		NDCGAt5:        qualityAgg.NDCGAt5,
		EnvyCountLeft:  totalEnvy, // 门禁只看总和，左右分解无意义
		EnvyCountRight: 0,
		TotalScenarios: qualityAgg.TotalScenarios,
	}
	gates := domain.Gates{
		HRAt3Min:     cfg.Gates().HRAt3Min,
		NDCGAt5Min:   cfg.Gates().NDCGAt5Min,
		TotalEnvyMax: cfg.Gates().TotalEnvyMax,
	}
	passed := gateReport.PassesGates(&gates)

	if *asJSON {
		payload := gateReport.ToMap()
		perBench := map[string]any{}
		for name, r := range reports {
			perBench[name] = r.ToMap()
		}
		payload["per_bench"] = perBench
		out, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "报告序列化失败: %v\n", err)
			return 2
		}
		fmt.Println(string(out))
	} else {
		fmt.Println("--- Mutual 评测报告（三场景 bench + 合成市场） ---")
		for _, name := range append([]string{"classic", "drift", "cold"}, "market") {
			r := reports[name]
			fmt.Printf("  %-8s HR@3=%.3f NDCG@5=%.3f envy=%d scenarios=%d\n",
				name, r.HRAt3, r.NDCGAt5, r.TotalEnvy(), r.TotalScenarios)
		}
		fmt.Printf("  门禁输入: HR@3=%.3f NDCG@5=%.3f total_envy=%d\n",
			gateReport.HRAt3, gateReport.NDCGAt5, totalEnvy)
		fmt.Printf("  门禁   : hr_at_3_min=%.2f ndcg_at_5_min=%.2f total_envy_max=%d\n",
			gates.HRAt3Min, gates.NDCGAt5Min, gates.TotalEnvyMax)
		verdict := "FAIL"
		if passed {
			verdict = "PASS"
		}
		fmt.Printf("  结果   : %s (%s)\n", verdict, map[bool]string{true: "通过门禁", false: "未达门禁"}[passed])
	}

	if *extended {
		blend := cfg.Blending()
		fmt.Println("--- 扩展陷阱套件（诊断，不计入门禁；生产姿态运行）---")
		for _, name := range bench.ExtendedScenarioNames {
			r, err := bench.RunExtendedScenario(name, bench.ScenarioOptions{
				Seed:                 *seed,
				NoiseScale:           *noiseScale,
				EmbedWeight:          blend.EmbedWeight,
				LLMWeight:            blend.LLMWeight,
				FallbackTopK:         3,
				HardConstraintFilter: cfg.MatchingHardFilter(),
			})
			if err != nil {
				fmt.Printf("  %-12s 运行失败: %v\n", name, err)
				continue
			}
			extra := ""
			if n, ok := r.Metadata["n_ineligible_pairs"].(int); ok && n > 0 {
				extra = fmt.Sprintf(" 资格排除=%d", n)
			}
			fmt.Printf("  %-12s HR@3=%.3f NDCG@5=%.3f envy=%d scenarios=%d%s\n",
				name, r.HRAt3, r.NDCGAt5, r.TotalEnvy(), r.TotalScenarios, extra)
		}
	}

	if *failOnGate && !passed {
		return 1
	}
	return 0
}

// cmdCalibrate 按评测历史做权重/prompt 校准（反馈注入）。
//
// 参数来源（CodeRabbit）：起点 blending / 基础 prompt / 窗口大小均取
// 自配置文件（--config，默认 config/default.yaml），不硬编码——与
// evaluate 的配置口径一致，YAML 调整后两者不再分歧。
func cmdCalibrate(args []string) int {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	configPath := fs.String("config", "config/default.yaml", "配置文件路径（校准起点与参数来源）")
	historyPath := fs.String("history", "", "评测历史 JSON 文件（时间升序）")
	embeddingOnly := fs.Bool("embedding-only", false, "只输出 prompt 校准块，不调权重")
	_ = fs.Parse(args)

	cfg, err := loadConfigOrDefault(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置加载失败: %v\n", err)
		return 2
	}

	if *historyPath == "" {
		fmt.Fprintln(os.Stderr, "--history 是必需参数")
		return 2
	}
	data, err := os.ReadFile(*historyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取历史文件失败: %v\n", err)
		return 2
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "解析历史文件失败: %v\n", err)
		return 2
	}
	history := make([]domain.EvaluationReport, 0, len(raw))
	for _, e := range raw {
		history = append(history, domain.EvaluationReport{
			HRAt1:          asFloat(e["hr_at_1"]),
			HRAt3:          asFloat(e["hr_at_3"]),
			HRAt5:          asFloat(e["hr_at_5"]),
			NDCGAt5:        asFloat(e["ndcg_at_5"]),
			EnvyCountLeft:  int(asFloat(e["envy_count_left"])),
			EnvyCountRight: int(asFloat(e["envy_count_right"])),
			TotalScenarios: int(asFloat(e["total_scenarios"])),
		})
	}
	if len(history) < 2 && !*embeddingOnly {
		fmt.Println("history 不足两条：权重校准需要 current+previous，输出 prompt 校准块。")
	}

	promptBlock := feedback.CalibratePrompt(cfg.Calibration().PromptBase, history, cfg.Calibration().Window)
	fmt.Println("=== Prompt 校准块 ===")
	fmt.Println(promptBlock)

	if !*embeddingOnly && len(history) >= 2 {
		blending := cfg.Blending() // 起点与 evaluate 同源（配置驱动）
		current := history[len(history)-1]
		previous := history[len(history)-2]
		newBlending := feedback.CalibrateWeights(blending, &current, &previous, 0)
		fmt.Println("=== 权重校准 ===")
		fmt.Printf("  %s -> %s\n",
			formatBlending(blending), formatBlending(newBlending))
	}
	return 0
}

// loadConfigOrDefault 加载配置；默认路径不存在时回落内置默认值
// （CI 在仓库根运行时命中 config/default.yaml）。
func loadConfigOrDefault(path string) (*config.Config, error) {
	if _, err := os.Stat(path); err != nil {
		if path == "config/default.yaml" && os.IsNotExist(err) {
			return config.Default()
		}
		return nil, fmt.Errorf("配置路径不可访问: %w", err)
	}
	return config.Load(path, nil)
}

func formatBlending(b engine.BlendingConfig) string {
	return fmt.Sprintf("map[embed_weight:%.2f llm_weight:%.2f]", b.EmbedWeight, b.LLMWeight)
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
