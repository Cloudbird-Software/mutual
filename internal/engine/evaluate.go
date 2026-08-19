package engine

import (
	"fmt"
	"math"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// EvaluateInput 是 evaluate 阶段的输入。
type EvaluateInput struct {
	// Predictions: 每个场景的推荐列表（按优先级降序）。
	Predictions [][]string
	// GroundTruth: 每个场景的唯一正例（与 Predictions 等长，
	// 不等长返回契约错误——静默截断会产生错误指标，qodo #6）。
	GroundTruth []string
	// PrefMatrix / MatchProb: 可选；两者齐备且非空时计算 envy 计数。
	PrefMatrix *domain.PrefMatrix
	MatchProb  domain.Matrix
}

// Evaluate 计算 HR@1/3/5、NDCG@5（推荐质量）+ envy 计数（互惠公平）
// （evaluate 阶段，spec/03-oracles.md 的评测 Oracle）。
//
// 指标语义（复用 AgentRecBench）：
//   - rank = ground_truth 在 predictions 中的 1-indexed 位置（未命中 ∞）；
//   - HR@K = 命中数（rank ≤ K） / 场景数；
//   - NDCG@5 = mean(1/log2(rank+1))，单 ground-truth → IDCG = 1。
//
// envy 计数（own-best 语义，与 CheckEnvy 逐位一致）：对每个实体 i，
// 若其他实体拿到的某个选项严格优于 i 自己最优的匹配，i 嫉妒之。
func Evaluate(in EvaluateInput) (domain.EvaluationReport, error) {
	if len(in.Predictions) != len(in.GroundTruth) {
		return domain.EvaluationReport{}, &domain.ContractError{
			Field:  "predictions/ground_truth",
			Reason: "长度不一致：评测输入畸形，拒绝计算（qodo #6）",
		}
	}
	total := len(in.Predictions)
	if total == 0 {
		return domain.EvaluationReport{
			Metadata: map[string]any{"prediction_lengths": []any{}},
		}, nil
	}

	hits1, hits3, hits5 := 0, 0, 0
	ndcg := 0.0
	for k, preds := range in.Predictions {
		rank := rankOf(in.GroundTruth[k], preds)
		if rank <= 1 {
			hits1++
		}
		if rank <= 3 {
			hits3++
		}
		if rank <= 5 {
			hits5++
		}
		ndcg += ndcgAt5(rank)
	}

	leftEnvy, rightEnvy := 0, 0
	if in.PrefMatrix != nil && in.MatchProb != nil &&
		in.MatchProb.Rows() > 0 && in.MatchProb.Cols() > 0 && anyAboveHalf(in.MatchProb) {
		// 形状契约（CodeRabbit）：PrefMatrix 与 MatchProb 是两个独立字段，
		// 不配套时 envyCount 的 pref[i][own[0]] 会越界 panic——与
		// predictions/ground_truth 同一契约纪律：畸形输入拒绝计算。
		if in.PrefMatrix.M() != in.MatchProb.Rows() || in.PrefMatrix.N() != in.MatchProb.Cols() {
			return domain.EvaluationReport{}, &domain.ContractError{
				Field: "pref_matrix/match_prob",
				Reason: fmt.Sprintf("形状不一致：pref %d×%d vs match_prob %d×%d，envy 输入畸形，拒绝计算",
					in.PrefMatrix.M(), in.PrefMatrix.N(), in.MatchProb.Rows(), in.MatchProb.Cols()),
			}
		}
		leftEnvy = envyCount(in.PrefMatrix.PrefLeftToRight, in.MatchProb)
		// 右侧：转置视角（行 = right 侧，列 = left 侧）。
		transposed := transpose(in.MatchProb)
		rightEnvy = envyCount(in.PrefMatrix.PrefRightToLeft, transposed)
	}

	lengths := make([]any, total)
	for i, p := range in.Predictions {
		lengths[i] = len(p)
	}
	return domain.EvaluationReport{
		HRAt1:          float64(hits1) / float64(total),
		HRAt3:          float64(hits3) / float64(total),
		HRAt5:          float64(hits5) / float64(total),
		NDCGAt5:        ndcg / float64(total),
		EnvyCountLeft:  leftEnvy,
		EnvyCountRight: rightEnvy,
		TotalScenarios: total,
		Metadata:       map[string]any{"prediction_lengths": lengths},
	}, nil
}

// rankOf 返回 target 在 predictions 中的 1-indexed 位置；未命中 ∞。
func rankOf(target string, predictions []string) float64 {
	for i, p := range predictions {
		if p == target {
			return float64(i + 1)
		}
	}
	return math.Inf(1)
}

// ndcgAt5 单 ground-truth（IDCG=1）：rank ≤ 5 时 1/log2(rank+1)。
func ndcgAt5(rank float64) float64 {
	if rank <= 5 {
		return 1.0 / math.Log2(rank+1)
	}
	return 0
}

// envyCount 统计一侧的 envy 计数（own-best 语义）。
// pref 行 = envier 侧，列 = 被匹配侧；match_prob 与 pref 同形。
func envyCount(pref domain.Matrix, matchProb domain.Matrix) int {
	n := matchProb.Rows()
	matches := make([][]int, n)
	for i := 0; i < n; i++ {
		for j := range matchProb[i] {
			if matchProb[i][j] > 0.5 {
				matches[i] = append(matches[i], j)
			}
		}
	}
	count := 0
	for i, own := range matches {
		if len(own) == 0 {
			continue
		}
		ownBest := pref[i][own[0]]
		for _, j := range own {
			if pref[i][j] > ownBest {
				ownBest = pref[i][j]
			}
		}
		for iPrime, other := range matches {
			if iPrime == i {
				continue
			}
			envied := false
			for _, j := range other {
				if pref[i][j] > ownBest {
					envied = true
					break
				}
			}
			if envied {
				count++
			}
		}
	}
	return count
}

func anyAboveHalf(m domain.Matrix) bool {
	for _, row := range m {
		for _, v := range row {
			if v > 0.5 {
				return true
			}
		}
	}
	return false
}

func transpose(m domain.Matrix) domain.Matrix {
	rows, cols := m.Rows(), m.Cols()
	t := domain.NewMatrixZeros(cols, rows)
	for i := 0; i < rows; i++ {
		for j := 0; j < cols; j++ {
			t[j][i] = m[i][j]
		}
	}
	return t
}
