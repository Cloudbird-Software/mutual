package engine

import (
	"math"
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// denomBundle 构造单用户、双分节（vision/skills）各一个 2 维向量的
// bundle。zeroSection 为 true 时该分节向量取零范数（= 缺失 cell，
// 被剔除出分子分母）。
func denomBundle(zeroVision, zeroSkills bool) *domain.EmbeddingsBundle {
	vec := func(zero bool) domain.Vector {
		if zero {
			return domain.Vector{0, 0}
		}
		return domain.Vector{1, 0}
	}
	return &domain.EmbeddingsBundle{
		UserIDs:      []domain.UserID{"u"},
		SectionNames: []domain.SectionName{"vision", "skills"},
		Embeddings: domain.EmbeddingTensor{
			domain.UserEmbeddings{
				domain.SectionEmbeddings{vec(zeroVision)},
				domain.SectionEmbeddings{vec(zeroSkills)},
			},
		},
		Dim: 2,
	}
}

// TestDenomFloorGuardsTinyDenominator 逐 cell 有效分母 |Σw| < denomFloor
// → 拒绝除法（dir=0），防止自定义权重在个别 cell 上正负相消出极小
// 分母、把 numer 异常放大（CodeRabbit）。
func TestDenomFloorGuardsTinyDenominator(t *testing.T) {
	// 1.0 + (-0.9999999) = 1e-7 < denomFloor(1e-6)，cos 均为 1。
	res := ComputeSimilarity(denomBundle(false, false), nil, RecipeConfig{
		SectionWeights: map[string]float64{"vision": 1.0, "skills": -0.9999999},
	})
	if got := res.DirMatrix[0][0]; got != 0 {
		t.Fatalf("极小分母（1e-7）应拒绝除法得 0，got %g", got)
	}
}

// TestNegativeDenominatorKeepsPythonSemantics 负分母（|d| ≥ floor）
// 保留 Python 基线语义：单负权重 section 是刻意的惩罚项设计，
// 一律除法（dir = numer/denom = cos）。
func TestNegativeDenominatorKeepsPythonSemantics(t *testing.T) {
	// 仅 skills（-0.5）有效：denom = -0.5，numer = -0.5·cos=−0.5 → dir = 1。
	res := ComputeSimilarity(denomBundle(true, false), nil, RecipeConfig{
		SectionWeights: map[string]float64{"vision": 0.35, "skills": -0.5},
	})
	if got, want := res.DirMatrix[0][0], 1.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("负分母（|d|≥floor）应按 Python 语义除法得 1，got %g", got)
	}
}

// TestNormalWeightsDivideNormally 正常权重（golden 配置量级）不受
// denomFloor 影响——golden 逐位对拍路径的分母量级 ~1.2，永不触界。
func TestNormalWeightsDivideNormally(t *testing.T) {
	res := ComputeSimilarity(denomBundle(false, false), nil, RecipeConfig{
		SectionWeights: map[string]float64{"vision": 0.35, "project": 0.25, "skills": -0.10},
	})
	// skills 未配置分节向量缺失（bundle 只有 vision/skills；project 无 term），
	// denom = 0.35，cos=1 → dir = 1。
	if got, want := res.DirMatrix[0][0], 1.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("正常权重应正常除法得 1，got %g", got)
	}
}
