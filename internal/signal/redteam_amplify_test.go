package signal

// 红队攻击性测试（attacker C：欺诈夸大者）。
//
// 威胁模型：合规基准档案（诚实 mallory）经"系统性夸大 / 关键词堆砌"
// 后，在信号层获得不对等匹配优势；全链路（extract → score → match）
// 无任何事实校验点。本文件量化证明该攻击，只用导出 API
// （DirectionalScore/EmbedScore/Tokenize/FakeEmbedder）。
//
// 三个测试：
//   - TestRedTeamAmplifyExaggeration  夸大攻击量化（受害 cohort = golden 4 人）
//   - TestRedTeamAmplifyMockBlindSpot FakeEmbedder 内容盲（CI 门禁无法暴露攻击）
//   - TestRedTeamAmplifyNoVerify      全链路无事实核验（代码行引用论证）
//
// NSW 贪心匹配的本地复刻对应 engine.SolveMatch（match.go:86-144）：
// 候选边按 sqrt(pref_lr·pref_rl) 降序贪心 + b_max 对称度约束（同集）。

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
)

// ---- 受害 cohort 加载 ----

type goldenProfile struct {
	ID       string            `json:"id"`
	Sections map[string]string `json:"sections"`
}

func loadGoldenCohort(t *testing.T) []OrderedSections {
	t.Helper()
	ids := []string{"alice", "bob", "carol", "david"}
	out := make([]OrderedSections, 0, len(ids))
	for _, id := range ids {
		raw, err := os.ReadFile(fmt.Sprintf("../../golden/test_basic/%s.json", id))
		if err != nil {
			t.Fatalf("读取 golden 画像 %s: %v", id, err)
		}
		var p goldenProfile
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("解析 golden 画像 %s: %v", id, err)
		}
		if p.ID != id {
			t.Fatalf("golden 画像 id 不匹配: 文件 %s vs 内容 %s", id, p.ID)
		}
		for _, sec := range []string{"skills", "vision", "project", "needs"} {
			if strings.TrimSpace(p.Sections[sec]) == "" {
				t.Fatalf("golden 画像 %s 缺分节 %s", id, sec)
			}
		}
		out = append(out, OrderedSections{ID: p.ID, Sections: p.Sections})
	}
	return out
}

// ---- 攻击者档案 ----

// honestMallory 是合规基准档案（真实能力：初级前端）。
func honestMallory() map[string]string {
	return map[string]string{
		"skills":  "junior frontend developer with 2 years of react and css experience",
		"vision":  "become a well-rounded product engineer",
		"project": "a personal blog with a lightweight comment system",
		"needs":   "mentorship from senior engineers and feedback on backend architecture",
	}
}

// unionTokens 提取多个画像同一分节的去重 token 全集（排序保证确定性）。
func unionTokens(profiles []OrderedSections, section string) []string {
	set := map[string]bool{}
	for _, p := range profiles {
		for _, tok := range Tokenize(p.Sections[section]) {
			set[tok] = true
		}
	}
	return SortedKeys(set)
}

// buildAttackerMallory 构造夸大攻击档案（纯关键词堆砌，零事实基础）：
//   - needs  := 受害 cohort skills 的 token 并集（直击 0.6 权重主信号：
//     overlap(A.needs, B.skills)——把"我需要"伪造成"你们恰好会的一切"）
//   - skills := 受害 cohort needs 的 token 并集 ∪ 全领域专家关键词
//    （反向直击成员→mallory 的主信号）
//   - vision := 受害 cohort vision 并集（vision-vision 项 0.2）
//   - project:= 受害 cohort project 并集（project-skills 项 0.2）
func buildAttackerMallory(members []OrderedSections) map[string]string {
	skillsUnion := unionTokens(members, "skills")
	needsUnion := unionTokens(members, "needs")
	visionUnion := unionTokens(members, "vision")
	projectUnion := unionTokens(members, "project")

	expertKeywords := []string{
		"frontend", "backend", "fullstack", "ai", "machine", "learning",
		"design", "product", "marketing", "finance", "legal", "mobile",
		"devops", "security", "data", "science", "ux", "ui", "cloud",
		"blockchain", "software", "engineering", "development", "web",
		"digital", "platform", "app", "startup", "strategy", "leadership",
	}
	skillSet := map[string]bool{}
	for _, tok := range needsUnion {
		skillSet[tok] = true
	}
	for _, kw := range expertKeywords {
		skillSet[kw] = true
	}

	return map[string]string{
		"needs":   strings.Join(skillsUnion, " "),
		"skills":  strings.Join(SortedKeys(skillSet), " "),
		"vision":  strings.Join(visionUnion, " "),
		"project": strings.Join(projectUnion, " "),
	}
}

// ---- 指标 ----

func stats(vals []float64) (mn, mean float64, zeros, above03 int) {
	if len(vals) == 0 {
		return 0, 0, 0, 0
	}
	mn = vals[0]
	sum := 0.0
	for _, v := range vals {
		if v < mn {
			mn = v
		}
		sum += v
		if v == 0 {
			zeros++
		}
		if v > 0.3 {
			above03++
		}
	}
	return mn, sum / float64(len(vals)), zeros, above03
}

// matchEdge 是本地复刻 NSW 贪心的输出边。
type matchEdge struct {
	A, B string
	NSW  float64
}

// greedyNSWMatch 复刻 engine.SolveMatch 的同集贪心语义（match.go:86-144）：
// 全部无序对按 NSW=sqrt(pref_lr·pref_rl) 降序，b_max 对称度约束下贪心。
// nsw<=0 的对不参与（match.go:94 过滤）。返回匹配边（按贪心序）与度数。
func greedyNSWMatch(ids []string, dir map[string]map[string]float64, bMax int) (edges []matchEdge, degree map[string]int) {
	degree = map[string]int{}
	type cand struct {
		i, j int
		nsw  float64
	}
	var cands []cand
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a := dir[ids[i]][ids[j]]
			b := dir[ids[j]][ids[i]]
			p := a * b
			if p <= 0 {
				continue
			}
			cands = append(cands, cand{i, j, math.Sqrt(p)})
		}
	}
	sort.SliceStable(cands, func(x, y int) bool { return cands[x].nsw > cands[y].nsw })
	for _, c := range cands {
		if degree[ids[c.i]] < bMax && degree[ids[c.j]] < bMax {
			edges = append(edges, matchEdge{ids[c.i], ids[c.j], c.nsw})
			degree[ids[c.i]]++
			degree[ids[c.j]]++
		}
	}
	return edges, degree
}

// ---- 测试 1：夸大攻击量化 ----

func TestRedTeamAmplifyExaggeration(t *testing.T) {
	members := loadGoldenCohort(t)
	malloryHonest := honestMallory()
	malloryAttack := buildAttackerMallory(members)

	// 双向矩阵：dir[a][b] = DirectionalScore(a, b)。
	scoresFor := func(mallory map[string]string) map[string]map[string]float64 {
		dir := map[string]map[string]float64{}
		for _, m := range members {
			dir[m.ID] = map[string]float64{"mallory": DirectionalScore(m.Sections, mallory)}
		}
		dir["mallory"] = map[string]float64{}
		for _, m := range members {
			dir["mallory"][m.ID] = DirectionalScore(mallory, m.Sections)
		}
		return dir
	}
	baseDir := scoresFor(malloryHonest)
	atkDir := scoresFor(malloryAttack)

	// 成员间真实基线（与 mallory 无关，用于 zero-sum 对比）。
	honestDir := map[string]map[string]float64{}
	for _, a := range members {
		honestDir[a.ID] = map[string]float64{}
		for _, b := range members {
			if a.ID == b.ID {
				continue
			}
			honestDir[a.ID][b.ID] = DirectionalScore(a.Sections, b.Sections)
		}
	}

	// ---- 表 1：mallory 双向分数（基线 vs 攻击）----
	t.Log("==== 表1：mallory ↔ 成员 双向 DirectionalScore（基线 vs 夸大攻击）====")
	t.Log("每行左半 = mallory→member（mallory 获得价值分），右半 = member→mallory（成员给出的价值分）")
	var baseVals, atkVals []float64
	for _, m := range members {
		bM2m := baseDir["mallory"][m.ID] // mallory → member（mallory 的"获得价值"分）
		aM2m := atkDir["mallory"][m.ID]
		bm2M := baseDir[m.ID]["mallory"] // member → mallory（成员视角"给 mallory 价值"分）
		am2M := atkDir[m.ID]["mallory"]
		baseVals = append(baseVals, bM2m, bm2M)
		atkVals = append(atkVals, aM2m, am2M)
		t.Log(fmt.Sprintf("%-22s %.4f → %.4f (+%.4f, x%.1f)   %.4f → %.4f (+%.4f, x%.1f)",
			"mallory→"+m.ID, bM2m, aM2m, aM2m-bM2m, ratio(aM2m, bM2m),
			bm2M, am2M, am2M-bm2M, ratio(am2M, bm2M)))
	}

	bMinV, bMean, bZeros, bAbove03 := stats(baseVals)
	aMin, aMean, aZeros, aAbove03 := stats(atkVals)
	bMaxV := baseVals[0]
	for _, v := range baseVals {
		if v > bMaxV {
			bMaxV = v
		}
	}
	t.Log(fmt.Sprintf("mallory 8 个方向分统计: 基线 min=%.4f max=%.4f mean=%.4f 零分=%d/8 >0.3=%d/8 | 攻击 min=%.4f mean=%.4f 零分=%d/8 >0.3=%d/8",
		bMinV, bMaxV, bMean, bZeros, bAbove03, aMin, aMean, aZeros, aAbove03))
	t.Log("有效覆盖口径：基线 0/8 方向达到 0.3 量级（停用词痕量交集 0.02-0.15，事实上不可配）；攻击 8/8 全部 >0.46")

	// EmbedScore（TF 余弦 embedding 路，blending 的另一臂）同样被欺骗。
	var baseEmb, atkEmb []float64
	for _, m := range members {
		baseEmb = append(baseEmb, EmbedScore(malloryHonest, m.Sections))
		atkEmb = append(atkEmb, EmbedScore(malloryAttack, m.Sections))
	}
	_, bEMean, _, _ := stats(baseEmb)
	_, aEMean, _, aEAbove := stats(atkEmb)
	t.Log(fmt.Sprintf("EmbedScore mallory↔成员: 基线 mean=%.4f → 攻击 mean=%.4f（>0.3 共 %d/4）", bEMean, aEMean, aEAbove))

	// ---- 表 2：NSW 贪心匹配三场景（b_min=3 / b_max=3，config/default.yaml:65-66）----
	// 场景 A：无 mallory（4 人基线）；B：诚实 mallory；C：攻击 mallory。
	runScenario := func(label string, ids []string, dir map[string]map[string]float64) ([]matchEdge, map[string]int) {
		edges, degree := greedyNSWMatch(ids, dir, 3)
		var lines []string
		for _, e := range edges {
			lines = append(lines, fmt.Sprintf("%s-%s(%.3f)", e.A, e.B, e.NSW))
		}
		viol := []string{}
		for _, id := range ids {
			if degree[id] < 3 {
				viol = append(viol, fmt.Sprintf("%s(度=%d)", id, degree[id]))
			}
		}
		if len(viol) == 0 {
			viol = append(viol, "无")
		}
		t.Log(fmt.Sprintf("%s: %d 条边 [%s] | b_min 违例: %s", label, len(edges), strings.Join(lines, " "), strings.Join(viol, ",")))
		return edges, degree
	}

	ids4 := []string{"alice", "bob", "carol", "david"}
	ids5 := append(append([]string{}, ids4...), "mallory")

	// 场景 A：4 人真实基线。
	edgesA, _ := runScenario("场景A 4人无mallory ", ids4, honestDir)
	honestEdgesA := 0
	for _, e := range edgesA {
		if e.A != "mallory" && e.B != "mallory" {
			honestEdgesA++
		}
	}

	// 场景 B：加入诚实 mallory。
	dirB := map[string]map[string]float64{}
	for k, v := range honestDir {
		dirB[k] = map[string]float64{}
		for kk, vv := range v {
			dirB[k][kk] = vv
		}
	}
	dirB["mallory"] = baseDir["mallory"]
	for _, m := range members {
		dirB[m.ID]["mallory"] = baseDir[m.ID]["mallory"]
	}
	edgesB, degB := runScenario("场景B +诚实mallory", ids5, dirB)
	honestEdgesB := 0
	for _, e := range edgesB {
		if e.A != "mallory" && e.B != "mallory" {
			honestEdgesB++
		}
	}

	// 场景 C：加入攻击 mallory。
	dirC := map[string]map[string]float64{}
	for k, v := range honestDir {
		dirC[k] = map[string]float64{}
		for kk, vv := range v {
			dirC[k][kk] = vv
		}
	}
	dirC["mallory"] = atkDir["mallory"]
	for _, m := range members {
		dirC[m.ID]["mallory"] = atkDir[m.ID]["mallory"]
	}
	edgesC, degC := runScenario("场景C +攻击mallory", ids5, dirC)
	honestEdgesC, malloryDegC := 0, degC["mallory"]
	for _, e := range edgesC {
		if e.A != "mallory" && e.B != "mallory" {
			honestEdgesC++
		}
	}
	violC := []string{}
	for _, id := range ids5 {
		if degC[id] < 3 {
			violC = append(violC, fmt.Sprintf("%s(度=%d)", id, degC[id]))
		}
	}
	t.Log(fmt.Sprintf("zero-sum: 诚实成员间匹配边 %d(场景A) → %d(场景C)；攻击 mallory 度=%d；场景C b_min 违例: %v",
		honestEdgesA, honestEdgesC, malloryDegC, violC))

	// ---- 断言：攻击有效性 ----
	if !(aMin > bMinV && aMean > 3*bMean) {
		t.Errorf("夸大攻击未获得显著分数优势: 攻击 min=%.4f/mean=%.4f vs 基线 min=%.4f/mean=%.4f", aMin, aMean, bMinV, bMean)
	}
	if aMin <= bMaxV {
		t.Errorf("非全面碾压: 攻击最低方向分 %.4f 应高于基线最高方向分 %.4f", aMin, bMaxV)
	}
	if !(aZeros == 0 && bZeros > 0) {
		t.Logf("零分覆盖: 基线零分=%d, 攻击零分=%d（覆盖面扩大证据）", bZeros, aZeros)
	}
	if malloryDegC < 3 {
		t.Errorf("攻击 mallory 度=%d < b_min=3：夸大未欺骗性满足覆盖约束（攻击失败？）", malloryDegC)
	}
	if degB["mallory"] >= 3 {
		t.Errorf("诚实 mallory 度=%d ≥ b_min=3：基线场景不应满足覆盖约束", degB["mallory"])
	}
	if honestEdgesC >= honestEdgesA {
		t.Errorf("攻击者未挤出诚实配对: 诚实边 %d → %d", honestEdgesA, honestEdgesC)
	}
	// 攻击 mallory 的每条边 NSW 必须压过诚实成员间的真实边（排序占位）。
	minAtk := math.MaxFloat64
	for _, m := range members {
		nsw := math.Sqrt(atkDir["mallory"][m.ID] * atkDir[m.ID]["mallory"])
		if nsw < minAtk {
			minAtk = nsw
		}
	}
	maxHonestNSW := 0.0
	for i := 0; i < len(members); i++ {
		for j := i + 1; j < len(members); j++ {
			nsw := math.Sqrt(honestDir[members[i].ID][members[j].ID] * honestDir[members[j].ID][members[i].ID])
			if nsw > maxHonestNSW {
				maxHonestNSW = nsw
			}
		}
	}
	t.Log(fmt.Sprintf("排序占位: 攻击 mallory 最弱边 NSW=%.4f vs 诚实成员间最强边 NSW=%.4f", minAtk, maxHonestNSW))
	if minAtk <= maxHonestNSW {
		t.Errorf("攻击边未全面压过诚实真实边: %.4f vs %.4f", minAtk, maxHonestNSW)
	}
}

func ratio(new, old float64) float64 {
	if old <= 1e-9 {
		if new <= 1e-9 {
			return 1
		}
		return math.Inf(1)
	}
	return new / old
}

// ---- 测试 2：Mock embedder 内容盲 ----

func vecCos(a, b []float64) float64 {
	dot, na, nb := 0.0, 0.0, 0.0
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

func TestRedTeamAmplifyMockBlindSpot(t *testing.T) {
	sameA := "I love painting and drawing colorful pictures"
	sameB := "I enjoy creating art with paints and canvases" // 语义同义改写
	unrelA := "Kubernetes orchestrates containerized workloads across clusters"
	unrelB := "The stock market rallied after the interest rate cut"

	vecs := (FakeEmbedder{}).Embed([]string{sameA, sameB, unrelA, unrelB})
	labels := []string{"同义A", "同义B", "无关A", "无关B"}

	t.Log("==== FakeEmbedder 128 维向量 cosine 矩阵（hash 播种随机向量）====")
	for i := range labels {
		row := []string{}
		for j := range labels {
			row = append(row, fmt.Sprintf("%7.4f", vecCos(vecs[i], vecs[j])))
		}
		t.Log(fmt.Sprintf("%-6s %v", labels[i], row))
	}

	cosSame := vecCos(vecs[0], vecs[1])          // 语义相同对
	unrelated := []float64{
		vecCos(vecs[0], vecs[2]), vecCos(vecs[0], vecs[3]),
		vecCos(vecs[1], vecs[2]), vecCos(vecs[1], vecs[3]),
	}
	maxUnrel := unrelated[0]
	for _, v := range unrelated {
		if v > maxUnrel {
			maxUnrel = v
		}
	}
	t.Log(fmt.Sprintf("语义同义对 cos=%.4f；语义无关对最大 cos=%.4f（真实 embedder 下同义对应 ≥0.7 且显著高于无关对）", cosSame, maxUnrel))

	if cosSame >= 0.5 {
		t.Errorf("内容盲反证失败：同义对 cos=%.4f 不应接近真实 embedder 量级（≥0.5）", cosSame)
	}
	if cosSame > maxUnrel+0.1 {
		t.Errorf("FakeEmbedder 似乎对语义敏感：同义对 %.4f 显著高于无关对最大值 %.4f", cosSame, maxUnrel)
	} else {
		t.Log("结论：FakeEmbedder 与语义无关（内容盲）——堆砌/虚构文本在 CI mock 下与正常文本不可区分，离线门禁无法暴露夸大攻击")
	}
}

// ---- 测试 3：全链路无事实核验（代码引用论证）----

func TestRedTeamAmplifyNoVerify(t *testing.T) {
	type src struct {
		path string
		text string
	}
	srcs := []src{
		{"../engine/extract.go", ""},
		{"../../baml_src/extract.baml", ""},
		{"../../baml_src/score.baml", ""},
	}
	for i := range srcs {
		raw, err := os.ReadFile(srcs[i].path)
		if err != nil {
			t.Fatalf("读取 %s: %v", srcs[i].path, err)
		}
		srcs[i].text = strings.ToLower(string(raw))
	}

	// 指令注入防护存在（text_block 隔离）——防护目标只有"指令"，没有"事实"。
	injectGuard := []string{"ignore any instructions", "untrusted user data"}
	for _, s := range srcs {
		if strings.Contains(s.text, injectGuard[0]) && strings.Contains(s.text, injectGuard[1]) {
			t.Log(fmt.Sprintf("%s: 存在注入隔离（IGNORE any instructions + UNTRUSTED USER DATA）——仅防指令注入", s.path))
		}
	}

	// 事实核验词汇全链路零命中（大小写不敏感扫描）。
	verifyWords := []string{"verify", "verif", "fact-check", "factcheck", "credib", "eviden", "corrobor", "ground truth", "factuality"}
	t.Log("==== 事实核验关键词扫描（extract.go / extract.baml / score.baml）====")
	totalHits := 0
	for _, s := range srcs {
		hits := []string{}
		for _, w := range verifyWords {
			if strings.Contains(s.text, w) {
				hits = append(hits, w)
			}
		}
		totalHits += len(hits)
		if len(hits) == 0 {
			t.Log(fmt.Sprintf("%s: 0 命中（无任何事实核验逻辑）", s.path))
		} else {
			t.Logf("%s: 命中 %v（需人工确认是否为真核验）", s.path, hits)
		}
	}

	// 引用关键行（见下方报告）：extract 只查非空非占位（isPresent），
	// baml 契约只做语义分节与双向打分，无声明-证据对照。
	t.Log("关键行引用：")
	t.Log("  engine/extract.go:34-41  分节缺失 → NotSpecified 占位；有效 = isPresent（仅非空非占位，extract.go:97-99），无真伪判断")
	t.Log("  baml_src/extract.baml:11-15  四分节 @description 只要求 \"Use 'Not specified' if not found\"——无事实核验指令")
	t.Log("  baml_src/extract.baml:22-29  SECURITY 段只防指令注入，不防内容欺诈（夸大的事实声明作为正常数据通过）")
	t.Log("  baml_src/score.baml:32-51  打分 prompt 直接消费画像声明；a_to_b/b_to_a 无可信度维度")
	t.Log("  engine/match.go:86-144  NSW 贪心只看分数，分数只看 token 重叠——堆砌关键词 = 直接抬分")

	if totalHits > 0 {
		t.Errorf("扫描到疑似核验逻辑 %d 处——若为真核验则本攻击论证失效，需复核", totalHits)
	}
}
