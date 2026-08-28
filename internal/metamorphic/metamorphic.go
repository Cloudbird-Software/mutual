// Package metamorphic 实现推荐链路的蜕变测试（metamorphic testing）。
//
// 思想（Murphy et al. 搜索引擎蜕变测试；无 oracle 系统的测试范式）：
// 不断言绝对输出，而是断言"变换输入后输出应如何变化"。本包针对
// surrogate 信号 + NSW 求解的离线链路定义 8 条蜕变关系（MR），
// 全部确定性（无噪声流），可常驻 CI：
//
//	MR-1a 唯一噪声不变性：每画像追加唯一噪声 token（纯范数稀释）→
//	      每成员 top-1 与基线一致（≥90%）、Kendall tau ≥ 0.9
//	MR-2  重复不变性：分节文本翻倍（余弦尺度不变）→ 排序逐位一致
//	MR-3  泛化降级：pool 画像泛词化 → 对有信号的对，NSW 严格下降
//	      （≥90%；零词面对已处地板，无从度量，n/a）
//	MR-4  堆砌反超率（测量型，不断言）：复述 member needs 的堆砌
//	      pool 反超黄金对的比率——词法盲区量化，LLM 契约层由 v3 防守
//	MR-5  干扰者不偷位：插入无关 pool 不改变既有相对排序（tau=1），
//	      且对有信号的成员严格低于黄金（平局不算偷位）
//	MR-6  克隆确定性：pool 克隆后两次求解边集语义一致（排序全序 +
//	      平局 (i,j) 字典序，确定性是本仓铁律）
//	MR-7  已知值阶梯：重叠 token 数 k=0..5 → NSW 严格单调
//	      （构造性已知真值 oracle）
//	MR-8  排除对 honored：excludedPairs 中的对绝不进入候选选择
//
// 已知盲区（测量型 MR 量化，非 bug）：共享噪声的幻影交集（MR-1b）、
// 词面堆砌反超（MR-4）、零词面黄金对的干扰者越位（MR-5 零基线特例）。
// 三者的 LLM 契约层防守由 baml_src/score.baml v3 判断纪律承担
// （合成陷阱集 A/B 命中率 70.8%→91.7%，见 docs/experiments/）。
package metamorphic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
	"github.com/Cloudbird-Software/mutual/internal/signal"
)

// Scenario 是最小场景（id → 分节），ground_truth: member → pool。
type Scenario struct {
	Members     []signal.OrderedSections
	Pool        []signal.OrderedSections
	GroundTruth map[string]string
}

// ParseScenario 把 members/pool JSON object（id → 四节 map）解析为
// 保序 Scenario（噪声流依赖文件序，与 bench 同规）。
func ParseScenario(membersRaw, poolRaw json.RawMessage, gt map[string]string) (*Scenario, error) {
	members, err := parseOrdered(membersRaw)
	if err != nil {
		return nil, fmt.Errorf("members: %w", err)
	}
	pool, err := parseOrdered(poolRaw)
	if err != nil {
		return nil, fmt.Errorf("pool: %w", err)
	}
	return &Scenario{Members: members, Pool: pool, GroundTruth: gt}, nil
}

// parseOrdered 保序解析 JSON object（id → sections map）。
// 包内自带（bench.OrderedSectionsMap 在 L2，本包不得反向依赖）。
func parseOrdered(raw json.RawMessage) ([]signal.OrderedSections, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("期望 JSON object")
	}
	var out []signal.OrderedSections
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var sections map[string]string
		if err := dec.Decode(&sections); err != nil {
			return nil, fmt.Errorf("%q: %w", key, err)
		}
		out = append(out, signal.OrderedSections{ID: key, Sections: sections})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneScenario(s *Scenario) *Scenario {
	out := &Scenario{
		Members:     append([]signal.OrderedSections(nil), s.Members...),
		Pool:        append([]signal.OrderedSections(nil), s.Pool...),
		GroundTruth: map[string]string{},
	}
	for i := range out.Members {
		out.Members[i].Sections = cloneSections(s.Members[i].Sections)
	}
	for i := range out.Pool {
		out.Pool[i].Sections = cloneSections(s.Pool[i].Sections)
	}
	for k, v := range s.GroundTruth {
		out.GroundTruth[k] = v
	}
	return out
}

func cloneSections(s map[string]string) map[string]string {
	out := make(map[string]string, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// MRResult 是单条 MR 的判定。
type MRResult struct {
	Name   string
	Pass   bool
	Detail string
}

// ---------------------------------------------------------------------------
// 信号与求解（确定性，无噪声）
// ---------------------------------------------------------------------------

func prefOf(s *Scenario) *domain.PrefMatrix {
	mem := idsOf(s.Members)
	pool := idsOf(s.Pool)
	pm := domain.NewPrefMatrix(mem, pool)
	for i, m := range s.Members {
		for j, p := range s.Pool {
			pm.PrefLeftToRight[i][j] = signal.DirectionalScore(m.Sections, p.Sections)
			pm.PrefRightToLeft[j][i] = signal.DirectionalScore(p.Sections, m.Sections)
		}
	}
	return pm
}

func solve(pm *domain.PrefMatrix, poolBMax int) engine.MatchOutcome {
	ptr := poolBMax
	return engine.SolveMatch(pm, engine.MatchingConfig{BMax: 3, PoolBMax: &ptr},
		engine.BlendingConfig{EmbedWeight: 0.5, LLMWeight: 0.5})
}

func idsOf(list []signal.OrderedSections) []domain.UserID {
	out := make([]domain.UserID, len(list))
	for i, s := range list {
		out[i] = domain.UserID(s.ID)
	}
	return out
}

// memberRanking 返回 member 行的 NSW 降序（平局 pid 降序，确定性）。
func memberRanking(pm *domain.PrefMatrix, mi int) []string {
	type cand struct {
		id  string
		nsw float64
	}
	var cs []cand
	for j := 0; j < pm.N(); j++ {
		nsw := math.Sqrt(max0(pm.PrefLeftToRight[mi][j]) * max0(pm.PrefRightToLeft[j][mi]))
		cs = append(cs, cand{string(pm.RightIDs[j]), nsw})
	}
	sort.SliceStable(cs, func(a, b int) bool {
		if cs[a].nsw != cs[b].nsw {
			return cs[a].nsw > cs[b].nsw
		}
		return cs[a].id > cs[b].id
	})
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.id
	}
	return out
}

func max0(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

func idxOf(list []signal.OrderedSections, id string) int {
	for i, s := range list {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// kendall 计算以基准序为准的一致性比率（并列按序取位置）。
func kendall(base, got []string) float64 {
	pos := map[string]int{}
	for i, x := range base {
		pos[x] = i
	}
	var seq []int
	for _, y := range got {
		if i, ok := pos[y]; ok {
			seq = append(seq, i)
		}
	}
	if len(seq) < 2 {
		return 1
	}
	conc, disc := 0, 0
	for i := 0; i < len(seq); i++ {
		for j := i + 1; j < len(seq); j++ {
			if seq[i] < seq[j] {
				conc++
			} else {
				disc++
			}
		}
	}
	return float64(conc) / float64(conc+disc)
}

// ---------------------------------------------------------------------------
// 变异算子
// ---------------------------------------------------------------------------

// MutateUniqueNoise 每画像逐节追加唯一噪声 token（跨画像不相交）。
func MutateUniqueNoise(s *Scenario, k int) {
	mut := func(list []signal.OrderedSections, tag string) {
		for i := range list {
			for name := range list[i].Sections {
				extra := ""
				for j := 0; j < k; j++ {
					extra += fmt.Sprintf(" nz%si%s%d", tag, name, j)
				}
				list[i].Sections[name] += extra
			}
		}
	}
	mut(s.Members, "m")
	mut(s.Pool, "p")
}

// MutateRepeat 每节文本翻倍。
func MutateRepeat(s *Scenario) {
	for i := range s.Members {
		for name, txt := range s.Members[i].Sections {
			s.Members[i].Sections[name] = txt + " " + txt
		}
	}
	for i := range s.Pool {
		for name, txt := range s.Pool[i].Sections {
			s.Pool[i].Sections[name] = txt + " " + txt
		}
	}
}

const vagueSkills = "various professional services many years experience wide network general support"

// MutateVagueifyPool 全体 pool 的 skills/project 泛词化。
func MutateVagueifyPool(s *Scenario) {
	for i := range s.Pool {
		s.Pool[i].Sections["skills"] = vagueSkills
		s.Pool[i].Sections["project"] = "several ongoing initiatives"
	}
}

// MutateDistractor 插入无关 pool。
func MutateDistractor(s *Scenario) {
	s.Pool = append(s.Pool, signal.OrderedSections{ID: "pDISTRACT", Sections: map[string]string{
		"needs":   "archery lessons and pottery classes weekend hobby group",
		"project": "community garden renovation",
		"skills":  "watercolor painting birdwatching tai chi",
		"vision":  "mindful leisure for everyone",
	}})
}

// MutateClone 克隆全部 pool（同文本新 id）。
func MutateClone(s *Scenario) {
	extra := make([]signal.OrderedSections, 0, len(s.Pool))
	for _, p := range s.Pool {
		extra = append(extra, signal.OrderedSections{ID: p.ID + "_clone", Sections: cloneSections(p.Sections)})
	}
	s.Pool = append(s.Pool, extra...)
}

// ---------------------------------------------------------------------------
// MR 套件
// ---------------------------------------------------------------------------

// RunSuite 对场景执行全部 MR，返回逐条判定。
func RunSuite(s *Scenario) []MRResult {
	basePM := prefOf(s)
	baseRank := map[string][]string{}
	for i, m := range s.Members {
		baseRank[m.ID] = memberRanking(basePM, i)
	}
	var out []MRResult
	check := func(name string, ok bool, detail string) {
		out = append(out, MRResult{Name: name, Pass: ok, Detail: detail})
	}

	// MR-1a 唯一噪声不变性
	{
		d := cloneScenario(s)
		MutateUniqueNoise(d, 3)
		pm := prefOf(d)
		keep, tauSum, n := 0, 0.0, 0
		for i, m := range d.Members {
			r := memberRanking(pm, i)
			if len(r) > 0 && len(baseRank[m.ID]) > 0 && r[0] == baseRank[m.ID][0] {
				keep++
			}
			tauSum += kendall(baseRank[m.ID], r)
			n++
		}
		check("MR-1a unique-noise-invariance",
			float64(keep)/float64(n) >= 0.9 && tauSum/float64(n) >= 0.9,
			fmt.Sprintf("top1不变=%d/%d mean_tau=%.3f", keep, n, tauSum/float64(n)))
	}

	// MR-2 重复不变性
	{
		d := cloneScenario(s)
		MutateRepeat(d)
		pm := prefOf(d)
		same, n := 0, 0
		for i, m := range d.Members {
			if ranksEqual(memberRanking(pm, i), baseRank[m.ID]) {
				same++
			}
			n++
		}
		check("MR-2 repeat-invariance", same == n, fmt.Sprintf("排序不变=%d/%d", same, n))
	}

	// MR-3 泛化降级（只对基线 NSW>0 的对；无可测对时 n/a 通过）
	{
		d := cloneScenario(s)
		MutateVagueifyPool(d)
		pmV := prefOf(d)
		dropped, total := 0, 0
		for i, m := range s.Members {
			gold, ok := s.GroundTruth[m.ID]
			if !ok {
				continue
			}
			j := idxOf(s.Pool, gold)
			nswBase := math.Sqrt(max0(basePM.PrefLeftToRight[i][j]) * max0(basePM.PrefRightToLeft[j][i]))
			if nswBase <= 0.001 {
				continue
			}
			nswAfter := math.Sqrt(max0(pmV.PrefLeftToRight[i][j]) * max0(pmV.PrefRightToLeft[j][i]))
			total++
			if nswAfter < nswBase {
				dropped++
			}
		}
		pass := total == 0 || float64(dropped)/float64(total) >= 0.9
		check("MR-3 vagueify-downgrade", pass, fmt.Sprintf("降分=%d/%d", dropped, total))
	}

	// MR-4 堆砌反超率（测量型）
	{
		d := cloneScenario(s)
		byID := map[string]signal.OrderedSections{}
		for _, p := range d.Pool {
			byID[p.ID] = p
		}
		for _, m := range d.Members {
			gold, ok := d.GroundTruth[m.ID]
			if !ok {
				continue
			}
			sp := signal.OrderedSections{ID: gold + "_SPAM", Sections: cloneSections(byID[gold].Sections)}
			sp.Sections["skills"] = m.Sections["needs"] + " premium solutions best quality"
			d.Pool = append(d.Pool, sp)
		}
		pmS := prefOf(d)
		beat, total := 0, 0
		for i, m := range d.Members {
			gold, ok := d.GroundTruth[m.ID]
			if !ok {
				continue
			}
			jG, jS := idxOf(d.Pool, gold), idxOf(d.Pool, gold+"_SPAM")
			nswG := math.Sqrt(max0(pmS.PrefLeftToRight[i][jG]) * max0(pmS.PrefRightToLeft[jG][i]))
			nswS := math.Sqrt(max0(pmS.PrefLeftToRight[i][jS]) * max0(pmS.PrefRightToLeft[jS][i]))
			total++
			if nswS > nswG {
				beat++
			}
		}
		check("MR-4 spam-beat-rate(measure)", true,
			fmt.Sprintf("堆砌反超=%d/%d（词法盲区量化，LLM 契约层 v3 防守）", beat, total))
	}

	// MR-5 干扰者不偷位（零基线黄金成员不计入；平局不算偷位）
	{
		d := cloneScenario(s)
		MutateDistractor(d)
		pm := prefOf(d)
		below, tauOK, n := 0, 0.0, 0
		for i, m := range d.Members {
			r := memberRanking(pm, i)
			var shared []string
			for _, x := range r {
				if x != "pDISTRACT" {
					shared = append(shared, x)
				}
			}
			tauOK += kendall(baseRank[m.ID], shared)
			gold, hasGold := d.GroundTruth[m.ID]
			if !hasGold {
				continue
			}
			jG := idxOf(d.Pool, gold)
			nswG := math.Sqrt(max0(basePM.PrefLeftToRight[i][jG]) * max0(basePM.PrefRightToLeft[jG][i]))
			if nswG <= 0.001 {
				continue // 零基线：任何非零噪声候选都"高于"黄金，属词法地板（MR-1b 域）
			}
			n++
			jD := idxOf(d.Pool, "pDISTRACT")
			nswD := math.Sqrt(max0(pm.PrefLeftToRight[i][jD]) * max0(pm.PrefRightToLeft[jD][i]))
			if nswD <= nswG {
				below++
			}
		}
		pass5 := n == 0 || float64(below)/float64(n) >= 0.95
		check("MR-5 distractor-no-steal", pass5 && tauOK/float64(len(d.Members)) == 1.0,
			fmt.Sprintf("不越位=%d/%d tau=%.4f", below, n, tauOK/float64(len(d.Members))))
	}

	// MR-6 克隆确定性（语义比较；Edge 含指针不得用 ==）
	{
		d := cloneScenario(s)
		MutateClone(d)
		o1 := solve(prefOf(d), 1)
		o2 := solve(prefOf(d), 1)
		same := len(o1.Edges) == len(o2.Edges)
		if same {
			for k := range o1.Edges {
				if !edgeSemEq(o1.Edges[k], o2.Edges[k]) {
					same = false
					break
				}
			}
		}
		check("MR-6 clone-determinism", same, fmt.Sprintf("n_edges=%d 语义一致=%v", len(o1.Edges), same))
	}

	// MR-7 已知值阶梯
	{
		res := ladderNSW()
		mono := true
		for i := 1; i < len(res); i++ {
			if res[i] <= res[i-1] {
				mono = false
			}
		}
		check("MR-7 ladder-monotone", mono, fmt.Sprintf("nsw=%v", res))
	}

	// MR-8 排除对 honored
	{
		sim := &domain.SimilarityResult{
			SourceIDs:   idsOf(s.Members),
			TargetIDs:   idsOf(s.Pool),
			FusedMatrix: fusedOf(s),
		}
		excluded := map[domain.PairID]bool{}
		if len(s.Members) > 0 && len(s.Pool) > 0 {
			excluded[domain.StablePairID(domain.UserID(s.Members[0].ID), domain.UserID(s.Pool[0].ID))] = true
		}
		sel := engine.SelectPairs(sim, engine.SelectBudgets{}, excluded)
		viol := 0
		for _, c := range sel {
			if excluded[domain.StablePairID(c.User1, c.User2)] {
				viol++
			}
		}
		check("MR-8 excluded-pairs-honored", viol == 0, fmt.Sprintf("违规=%d selected=%d", viol, len(sel)))
	}

	return out
}

func fusedOf(s *Scenario) domain.Matrix {
	m, n := len(s.Members), len(s.Pool)
	fused := domain.NewMatrixZeros(m, n)
	for i, mm := range s.Members {
		for j, pp := range s.Pool {
			if mm.ID == pp.ID {
				continue
			}
			fused[i][j] = signal.EmbedScore(mm.Sections, pp.Sections)
		}
	}
	return fused
}

func ranksEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func edgeSemEq(a, b domain.Edge) bool {
	f := func(x, y *float64) bool {
		if (x == nil) != (y == nil) {
			return false
		}
		return x == nil || *x == *y
	}
	return a.User1 == b.User1 && a.User2 == b.User2 && a.PairID == b.PairID &&
		a.FinalWeight == b.FinalWeight && f(a.LLMScoreAToB, b.LLMScoreAToB) &&
		f(a.LLMScoreBToA, b.LLMScoreBToA)
}

// ladderNSW 构造 k=0..5 重叠阶梯，返回各 k 的 NSW（构造性已知真值）。
func ladderNSW() []float64 {
	anchors := []string{"alpha", "bravo", "delta", "golf", "kilo"}
	mem := signal.OrderedSections{ID: "mK", Sections: map[string]string{
		"needs": "need " + join(anchors), "project": "ladder program",
		"skills": "ladder craft", "vision": "ladder vision"}}
	var pools []signal.OrderedSections
	for k := 0; k <= 5; k++ {
		pools = append(pools, signal.OrderedSections{ID: fmt.Sprintf("pK%d", k),
			Sections: map[string]string{
				"needs":   fmt.Sprintf("need uniq%d", k),
				"project": fmt.Sprintf("service uniqB%d", k),
				"skills":  join(anchors[:k]) + fmt.Sprintf(" uniqS%d", k),
				"vision":  fmt.Sprintf("vision uniqV%d", k),
			}})
	}
	pm := prefOf(&Scenario{Members: []signal.OrderedSections{mem}, Pool: pools})
	var out []float64
	for j := range pools {
		out = append(out, math.Sqrt(max0(pm.PrefLeftToRight[0][j])*max0(pm.PrefRightToLeft[j][0])))
	}
	return out
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
