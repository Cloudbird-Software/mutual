// Package engine 实现管线的确定性核心算法（纯变换，无 IO）。
//
// 与 Python 基线（src/mutual/*.py）逐阶段对应：
//
//	similarity → select → score → pre_matrix → match → evaluate → report
//
// 纯度契约（CLAUDE.md §2.3 铁律）：本包函数只做数据变换，
// 不碰文件系统 / 网络 / 时钟；IO 一律在 pipeline（adapter）层。
// LLM 依赖以 consumer-defined 接口注入（llm.go），engine 不 import
// 任何具体 LLM 实现。
//
// 等价性论证：golden 差分测试（internal/goldentest）对拍 Python 基线
// 的捕获输出（golden/ 目录），逐位一致是重写正确性的唯一证据；
// 为此浮点求和遵循 NumPy 语义（pairwise summation，internal/num）。
package engine

import (
	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/num"
)

// eps 与 Python 侧 _EPS 一致（零范数判定 / 分母安全阈值）。
const eps = 1e-12

// denomFloor 是逐 cell 有效分母的绝对值下界（CodeRabbit）：全局权重和
// 为正（config 加载期已校验）不保证每个 cell 的 Σw_t 安全——自定义
// 权重可在个别 cell 上让正负权重相消出极小分母，dir 被异常放大。
// |d| < denomFloor 的 cell 视为无有效信号（dir=0）；golden 配置的分母
// 量级 ~1.2，永不触界，逐位对拍不受影响。
const denomFloor = 1e-6

// WeightEntry 是保序的权重项（Key → Value）。
// 用切片而非 map：融合是浮点累加，term 顺序影响末位精度，
// 必须与 Python 侧 dict 的 YAML 插入顺序一致（golden 逐位对拍）。
type WeightEntry struct {
	Key   string
	Value float64
}

// RecipeConfig 是相似度融合配方（config["recipe"]）。
//
// SectionWeights: 分节自身项权重 {section: w}（迭代顺序按
// source.SectionNames，无累加顺序问题）；
// CrossSectionWeights: 方向性跨节项权重（保序），键 "X_Y" 的语义为
// source 的 X ↔ target 的 Y（如 needs_skills = A 的 needs 对 B 的
// skills），不做对称化（spec/05-boundaries.md §2）。
type RecipeConfig struct {
	SectionWeights      map[string]float64
	CrossSectionWeights []WeightEntry
}

// ComputeSimilarity 计算方向性相似度矩阵（similarity 阶段）。
//
// target == nil 时为 N×N 方阵模式（source 对自身，legacy
// (dir+dir.T)/2 对称化）；否则 M×N 矩形模式（query/batch）。
//
// 融合公式（mask + 分母修正，spec/05-boundaries.md §1）：
//
//	dir[i][j] = Σ_t w_t·cos_t[i][j] / Σ_t w_t
//
// t 只计双侧该 term 均有效的项；无有效项 → 0。缺失分节是中性
// （mask + 分母修正），不是零——零范数向量（缺失 cell）被剔除出
// 分子分母，避免把"没有信息"当作"相似度为 0"。
//
// term 分两类：
//   - 分节自身项：SectionWeights[s] · cos(src_s[i], tgt_s[j])；
//   - 方向性跨节项：CrossSectionWeights["X_Y"] · cos(src_X[i], tgt_Y[j])。
func ComputeSimilarity(source, target *domain.EmbeddingsBundle, recipe RecipeConfig) *domain.SimilarityResult {
	square := target == nil
	tgt := source
	if target != nil {
		tgt = target
	}

	srcIndex := indexSections(source.SectionNames)
	tgtIndex := indexSections(tgt.SectionNames)

	// 交集按 source 顺序（A-6）；两侧未配置权重的分节不参与融合。
	names := make([]string, 0, len(source.SectionNames))
	for _, n := range source.SectionNames {
		if _, ok := tgtIndex[string(n)]; ok {
			names = append(names, string(n))
		}
	}

	sideCache := map[*domain.EmbeddingsBundle]map[string]*sideMatrix{}
	sideOf := func(b *domain.EmbeddingsBundle, index map[string]int, name string) *sideMatrix {
		perBundle, ok := sideCache[b]
		if !ok {
			perBundle = map[string]*sideMatrix{}
			sideCache[b] = perBundle
		}
		if m, ok := perBundle[name]; ok {
			return m
		}
		m := candidateMatrix(b, index[name], name)
		perBundle[name] = m
		return m
	}

	type term struct {
		w   float64
		sim domain.Matrix
		ok  [][]bool
	}
	terms := []term{}

	addTerm := func(w float64, srcName, tgtName string) {
		su := sideOf(source, srcIndex, srcName)
		tu := sideOf(tgt, tgtIndex, tgtName)
		sim, ok := pooledSimilarity(su.unit, su.valid, tu.unit, tu.valid)
		terms = append(terms, term{w: w, sim: sim, ok: ok})
	}

	for _, name := range names {
		w := recipe.SectionWeights[name]
		if w == 0 {
			continue
		}
		addTerm(w, name, name)
	}
	// 跨节项按配置顺序迭代（WeightEntry 保序，见 RecipeConfig 文档）。
	for _, entry := range recipe.CrossSectionWeights {
		if entry.Value == 0 {
			continue
		}
		x, y, ok := splitCrossKey(entry.Key)
		if !ok {
			continue
		}
		if _, inSrc := srcIndex[x]; !inSrc {
			continue
		}
		if _, inTgt := tgtIndex[y]; !inTgt {
			continue
		}
		addTerm(entry.Value, x, y)
	}

	m, n := len(source.UserIDs), len(tgt.UserIDs)
	dir := domain.NewMatrixZeros(m, n)
	if len(terms) > 0 {
		numer := domain.NewMatrixZeros(m, n)
		denom := domain.NewMatrixZeros(m, n)
		for _, t := range terms {
			for i := 0; i < m; i++ {
				for j := 0; j < n; j++ {
					if t.ok[i][j] {
						numer[i][j] += t.w * t.sim[i][j]
						denom[i][j] += t.w
					}
				}
			}
		}
		for i := 0; i < m; i++ {
			for j := 0; j < n; j++ {
				d := denom[i][j]
				// |d| ≥ denomFloor 才除：极小分母会把 numer 异常放大
				// （自定义权重可构造 Σw_t≈0 的 cell，CodeRabbit）。
				// 负分母（|d| ≥ floor）保留：单负权重 section 是刻意的
				// 惩罚项设计（Python 基线同语义），golden 路径分母
				// 量级 ~1.2，不触本防护。
				if d >= denomFloor || d <= -denomFloor {
					dir[i][j] = numer[i][j] / d
				}
			}
		}
	}

	fused := dir.Clone()
	if square {
		// legacy 对称化：(dir + dir.T) / 2（仅为旧代码 bit-exact 兼容，A-4）。
		fused = domain.NewMatrixZeros(m, n)
		for i := 0; i < m; i++ {
			for j := 0; j < n; j++ {
				fused[i][j] = (dir[i][j] + dir[j][i]) / 2
			}
		}
	}

	return &domain.SimilarityResult{
		SourceIDs:   append([]domain.UserID(nil), source.UserIDs...),
		TargetIDs:   append([]domain.UserID(nil), tgt.UserIDs...),
		DirMatrix:   dir,
		FusedMatrix: fused,
	}
}

// sideMatrix 是某一侧（source/target）某分节的候选向量组：
// unit 为归一化后的 [N][C][D]，valid 为 [N][C]（零范数候选 = 缺失）。
type sideMatrix struct {
	unit  [][][]float64
	valid [][]bool
}

// candidateMatrix 取某分节的候选向量矩阵：section 原向量 + HyDE
// 描述符向量（A-5），返回归一化后的 [N][C][D] 与有效性 [N][C]。
func candidateMatrix(b *domain.EmbeddingsBundle, sectionIndex int, name string) *sideMatrix {
	n := b.N()
	var cand [][][]float64 // [N][C][D]
	for i := 0; i < n; i++ {
		var rows [][]float64
		for _, v := range b.Embeddings[i][sectionIndex] {
			rows = append(rows, v)
		}
		if hyde, ok := b.Hyde[domain.SectionName(name)]; ok && len(hyde) > i && len(hyde[i]) > 0 {
			for _, v := range hyde[i] {
				rows = append(rows, v)
			}
		}
		cand = append(cand, rows)
	}
	unit := make([][][]float64, n)
	valid := make([][]bool, n)
	for i := 0; i < n; i++ {
		unit[i] = make([][]float64, len(cand[i]))
		valid[i] = make([]bool, len(cand[i]))
		for c, v := range cand[i] {
			norm := num.Norm(v)
			ok := norm > eps
			valid[i][c] = ok
			unit[i][c] = make([]float64, len(v))
			div := 1.0
			if ok {
				div = norm
			}
			for d, x := range v {
				unit[i][c][d] = x / div
			}
		}
	}
	return &sideMatrix{unit: unit, valid: valid}
}

// pooledSimilarity 跨侧候选对 max-pool（A-5）：每对 (i, j) 取两侧
// 全部有效候选组合的 cosine 最大值；双侧均无有效候选 → 0 且 ok=false。
func pooledSimilarity(srcUnit [][][]float64, srcValid [][]bool, tgtUnit [][][]float64, tgtValid [][]bool) (domain.Matrix, [][]bool) {
	m := len(srcUnit)
	n := len(tgtUnit)
	sim := domain.NewMatrixZeros(m, n)
	ok := make([][]bool, m)
	for i := 0; i < m; i++ {
		ok[i] = make([]bool, n)
	}
	for i := 0; i < m; i++ {
		for j := 0; j < n; j++ {
			best := 0.0
			any := false
			for c, sv := range srcUnit[i] {
				if !srcValid[i][c] {
					continue
				}
				for k, tv := range tgtUnit[j] {
					if !tgtValid[j][k] {
						continue
					}
					dot := sequentialDot(sv, tv)
					if !any || dot > best {
						best = dot
						any = true
					}
				}
			}
			sim[i][j] = best
			ok[i][j] = any
		}
	}
	return sim, ok
}

// sequentialDot 顺序累加点积——与 numpy.einsum 的累加顺序一致
// （einsum 不做 pairwise 求和），golden 差分依赖此顺序。
func sequentialDot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// splitCrossKey 解析跨节权重键 "X_Y" → (X, Y)；无下划线返回 false。
func splitCrossKey(key string) (string, string, bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '_' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}

func indexSections(names []domain.SectionName) map[string]int {
	m := make(map[string]int, len(names))
	for i, n := range names {
		m[string(n)] = i
	}
	return m
}
