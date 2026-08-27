//go:build nightly

// 夜间大规模回归（生产级遗留项 #3，docs/experiments/2026-08-synthetic-data.md R8）：
// Go 原生生成三领域大规模语料（确定性，无数据文件依赖），守护：
//   - 蜕变关系在规模上成立（MR 套件 @ 同集 400 / 二部 300×450）
//   - 求解确定性在大规模下保持（同输入两次边集语义一致）
//   - 求解耗时预算（防复杂度回归悄悄上线）
//
// 运行：make nightly（= go test -tags=nightly ./internal/metamorphic/ -v）
// 日常 CI（make check）不含本文件——build tag 隔离。
package metamorphic

import (
	"fmt"
	"testing"
	"time"

	"github.com/Cloudbird-Software/mutual/internal/signal"
)

// ---------------------------------------------------------------------------
// 紧凑三领域生成器（lab/gen/generate.py 的 Go 最小移植，确定性）
// ---------------------------------------------------------------------------

var nlSectors = []string{"fintech", "manuf", "xborder", "branding", "biotech", "energy", "software", "culture"}

var nlAspectBase = []string{
	"lag free settlement", "cold chain tolerances", "vendor managed inventory", "high mix low volume",
	"zero trust posture", "audited change control", "multiregion failover", "graceful degradation",
	"metered billing", "reconciliation engine", "dispute workflow", "chargeback defense",
	"traceability serialization", "tamper evident packaging", "batch genealogy", "quarantine handling",
	"predictive maintenance", "spare parts pooling", "tooling lifecycle", "first pass yield",
	"bonded zone operations", "tariff engineering", "origin documentation", "cross docking",
	"reverse logistics", "shelf planogram compliance", "in store demo staffing", "trade marketing roi",
	"distributor financing", "grey market control",
}

var nlVariant = []string{
	"grid operators", "enterprise tenants", "apac chains", "regulated lenders", "public sector",
	"high assurance", "industrial sites", "cross straits", "bonded zones", "flagship stores",
	"research consortia", "utility scale", "franchise networks", "clinical networks", "campus fleets",
	"luxury verticals", "regional banks",
}

var nlVariantPara = []string{
	"power grids", "corporate occupants", "asia pacific retailers", "supervised banks", "government buyers",
	"safety critical", "factory parks", "strait crossings", "customs zones", "anchor shops",
	"academic alliances", "grid magnitude", "licensed chains", "hospital groups", "university fleets",
	"premium segments", "territory lenders",
}

func nlSlot(g int) string {
	return fmt.Sprintf("%s %s", nlAspectBase[g%30], nlVariant[(g/30)%17])
}

func nlSlotPara(g int) string {
	return fmt.Sprintf("%s %s", nlAspectBase[g%30], nlVariantPara[(g/30)%17])
}

// nlGen 生成三领域场景。sameSet=true 时同集（N×N），否则二部（members×pool）。
// paraphraseRate 控制黄金对 A 链路的同义改写率（词面零重叠）。
func nlGen(t *testing.T, name string, nMembers, nPool int, sameSet bool, paraphraseRate float64) *Scenario {
	t.Helper()
	type pairSides struct {
		member, pool map[string]string
	}
	var pairs []pairSides
	g := 0
	nGold := nMembers
	if sameSet {
		nGold = nMembers / 2
	}
	for i := 0; i < nGold; i++ {
		sector := nlSectors[i%len(nlSectors)]
		aA := nlSlot(g)
		aB := nlSlot(g + 1)
		g += 2
		paraphrased := float64(i%100)/100.0 < paraphraseRate
		aMember := aA
		if paraphrased {
			aMember = nlSlotPara(g - 2)
		}
		member := map[string]string{
			"needs":   aMember,
			"project": fmt.Sprintf("%s %s program phase %d", sector, aB, i%4+1),
			"skills":  fmt.Sprintf("%s %s support", aB, sector),
			"vision":  fmt.Sprintf("%s operators serious about %s", sector, aA),
		}
		pool := map[string]string{
			"needs":   aB,
			"project": fmt.Sprintf("%s %s service line", sector, aA),
			"skills":  fmt.Sprintf("%s %s delivery", aA, sector),
			"vision":  fmt.Sprintf("%s operators serious about %s", sector, aB),
		}
		pairs = append(pairs, pairSides{member: member, pool: pool})
	}
	// 组装（同集：双方都是 member，互为真值；二部：m↔p）
	sc := &Scenario{GroundTruth: map[string]string{}}
	for i, pr := range pairs {
		if sameSet {
			k1, k2 := fmt.Sprintf("m%03d", i*2), fmt.Sprintf("m%03d", i*2+1)
			sc.Members = append(sc.Members, signal.OrderedSections{ID: k1, Sections: pr.member},
				signal.OrderedSections{ID: k2, Sections: pr.pool})
			sc.GroundTruth[k1] = k2
			sc.GroundTruth[k2] = k1
		} else {
			mk, pk := fmt.Sprintf("m%03d", i), fmt.Sprintf("p%03d", i)
			sc.Members = append(sc.Members, signal.OrderedSections{ID: mk, Sections: pr.member})
			sc.Pool = append(sc.Pool, signal.OrderedSections{ID: pk, Sections: pr.pool})
			sc.GroundTruth[mk] = pk
		}
	}
	if sameSet {
		sc.Pool = append([]signal.OrderedSections(nil), sc.Members...)
		return sc
	}
	// 二部补足无真值 pool（复用槽位续排，保持唯一性）
	for j := len(sc.Pool); j < nPool; j++ {
		sector := nlSectors[j%len(nlSectors)]
		aA := nlSlot(g)
		g++
		sc.Pool = append(sc.Pool, signal.OrderedSections{ID: fmt.Sprintf("p%03d", j), Sections: map[string]string{
			"needs":   aA,
			"project": fmt.Sprintf("%s extra service line %d", sector, j),
			"skills":  fmt.Sprintf("%s %s delivery extra", aA, sector),
			"vision":  fmt.Sprintf("%s operators extra %d", sector, j),
		}})
	}
	return sc
}

// TestNightlyMRSuiteAtScale 蜕变关系在规模上成立（同集 400 / 二部 300×450）。
func TestNightlyMRSuiteAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过夜间规模套件")
	}
	cases := []struct {
		name string
		s    *Scenario
	}{
		{"sameset400", nlGen(t, "sameset400", 400, 400, true, 0.3)},
		{"bipartite300x450", nlGen(t, "bipartite", 300, 450, false, 0.4)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			for _, r := range RunSuite(tc.s) {
				if !r.Pass {
					t.Errorf("%s: %s — %s", tc.name, r.Name, r.Detail)
				}
			}
			if el := time.Since(start); el > 10*time.Minute {
				t.Errorf("MR 套件耗时超预算: %v", el.Round(time.Second))
			}
		})
	}
}

// TestNightlyDeterminismAtScale 大规模求解确定性（同输入两次边集语义一致）
// 与耗时预算（防复杂度回归）。
func TestNightlyDeterminismAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过夜间规模套件")
	}
	s := nlGen(t, "sameset400det", 400, 400, true, 0.3)
	start := time.Now()
	o1 := solve(prefOf(s), 1)
	el := time.Since(start)
	if el > 2*time.Minute {
		t.Errorf("400 人求解耗时超预算: %v", el.Round(time.Second))
	}
	o2 := solve(prefOf(s), 1)
	if len(o1.Edges) != len(o2.Edges) {
		t.Fatalf("边数不一致: %d vs %d", len(o1.Edges), len(o2.Edges))
	}
	for i := range o1.Edges {
		if !edgeSemEq(o1.Edges[i], o2.Edges[i]) {
			t.Fatalf("第 %d 条边语义不一致: %s", i, o1.Edges[i].PairID)
		}
	}
}
