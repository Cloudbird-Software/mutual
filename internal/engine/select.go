package engine

import (
	"sort"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// SelectBudgets 是候选对选择的预算约束（config["budgets"]）。
// nil 指针字段 = 不设上限（与 Python None 语义一致）。
type SelectBudgets struct {
	// PerProfileCap: 每用户最多入选对数
	// （max_n_llm_evaluations_per_profile，member 与 pool 侧同权）。
	PerProfileCap *int
	// GlobalCap: 入选对总数上限（max_pair_llm_calls，A-9：
	// 语义 = 入选 pair 总数上限，每 pair 至少一次 LLM 调用）。
	GlobalCap *int
}

// pairCandidate 是单个用户视角的候选（partner + fused 分数）。
type pairCandidate struct {
	other domain.UserID
	score float64
}

// SelectPairs 从相似度矩阵贪心轮转选择进入 LLM 精排的候选对
// （select 阶段，纯函数）。
//
// 约束（spec/05-boundaries.md §8、spec/02-stages.md §5）：
//   - per-profile cap：每个用户（双侧同权）最多入选 cap 对；
//   - global cap：总对数 ≤ GlobalCap；
//   - novelty：excludedPairs 中的 pair 不参与选择；
//   - 只保留正相似度对（fused 分数 ≤ 0 不选）；
//   - M×N 模式下重叠用户的自配对 (u,u) 排除。
//
// 轮转顺序与平局裁决（spec 沉默 A-7）：每轮按 source_ids 顺序轮流
// 为每个用户取其当前最优候选；分数平局取字典序较小的 partner；
// 返回列表按选择顺序排列。选择依据 = fused_matrix（A-8）。
func SelectPairs(similarity *domain.SimilarityResult, budgets SelectBudgets, excludedPairs map[domain.PairID]bool) []domain.CandidatePair {
	srcIDs := similarity.SourceIDs
	tgtIDs := similarity.TargetIDs
	fused := similarity.FusedMatrix
	square := similarity.IsSquare()

	// 每个源侧用户的候选列表（正相似度、非排除、非自配对）。
	candidates := map[domain.UserID][]pairCandidate{}
	for _, uid := range srcIDs {
		candidates[uid] = nil
	}
	for i, uid := range srcIDs {
		for j, other := range tgtIDs {
			if other == uid {
				continue
			}
			var score float64
			if square && i > j {
				score = fused[j][i] // 上三角镜像（对称 legacy 路径）
			} else {
				score = fused[i][j]
			}
			if score <= 0 {
				continue
			}
			if excludedPairs[domain.StablePairID(uid, other)] {
				continue
			}
			candidates[uid] = append(candidates[uid], pairCandidate{other: other, score: score})
		}
	}
	// 分数降序；平局取字典序较小 partner（A-7）。稳定排序保证确定性。
	for uid := range candidates {
		list := candidates[uid]
		sort.SliceStable(list, func(a, b int) bool {
			if list[a].score != list[b].score {
				return list[a].score > list[b].score
			}
			return list[a].other < list[b].other
		})
		candidates[uid] = list
	}

	counts := map[domain.UserID]int{}
	for _, uid := range srcIDs {
		counts[uid] = 0
	}
	for _, uid := range tgtIDs {
		counts[uid] = 0
	}
	atCap := func(uid domain.UserID) bool {
		return budgets.PerProfileCap != nil && counts[uid] >= *budgets.PerProfileCap
	}

	chosen := map[domain.PairID]bool{}
	var result []domain.CandidatePair
	total := 0

	for budgets.GlobalCap == nil || total < *budgets.GlobalCap {
		progressed := false
		for _, uid := range srcIDs {
			if budgets.GlobalCap != nil && total >= *budgets.GlobalCap {
				break
			}
			if atCap(uid) {
				continue
			}
			for _, c := range candidates[uid] {
				if atCap(c.other) {
					continue
				}
				pid := domain.StablePairID(uid, c.other)
				if chosen[pid] {
					continue
				}
				result = append(result, domain.NewCandidatePair(uid, c.other, c.score))
				chosen[pid] = true
				counts[uid]++
				counts[c.other]++
				total++
				progressed = true
				break
			}
		}
		if !progressed {
			break
		}
	}
	return result
}
