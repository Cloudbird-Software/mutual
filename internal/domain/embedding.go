package domain

// Vector 是一个 embedding 向量（维度 D 由 bundle 的 Dim 给出）。
type Vector []float64

// SectionEmbeddings 是单个用户在单个分节下的向量组
// （含 HyDE 描述向量时长度为 1 + n_descriptors，首元素为 section 本体）。
type SectionEmbeddings []Vector

// UserEmbeddings 是单个用户跨所有分节的向量组 [S][D...]。
type UserEmbeddings []SectionEmbeddings

// EmbeddingTensor 是全体用户的 embedding 张量 [N][S][D]。
type EmbeddingTensor []UserEmbeddings

// Matrix 是二维 float64 矩阵（相似度矩阵 / 偏好矩阵等）。
// 行列语义由使用方文档给出；构造时不强制形状，读取方负责校验。
type Matrix [][]float64

// Rows 返回行数（空矩阵为 0）。
func (m Matrix) Rows() int { return len(m) }

// Cols 返回列数（按第一行推断；空矩阵为 0）。
func (m Matrix) Cols() int {
	if len(m) == 0 {
		return 0
	}
	return len(m[0])
}

// At 返回 (i, j) 元素。越界 panic——契约违背应尽早暴露，
// 不做静默零值（AI 阅读友好：失败要响）。
func (m Matrix) At(i, j int) float64 { return m[i][j] }

// NewMatrixZeros 构造 rows×cols 的零矩阵。
func NewMatrixZeros(rows, cols int) Matrix {
	m := make(Matrix, rows)
	for i := range m {
		m[i] = make([]float64, cols)
	}
	return m
}

// Clone 返回深拷贝。
func (m Matrix) Clone() Matrix {
	out := make(Matrix, len(m))
	for i, row := range m {
		out[i] = append([]float64(nil), row...)
	}
	return out
}

// ToPlain 转为 [][]any（JSON 序列化用）。
func (m Matrix) ToPlain() [][]any {
	out := make([][]any, len(m))
	for i, row := range m {
		r := make([]any, len(row))
		for j, v := range row {
			r[j] = v
		}
		out[i] = r
	}
	return out
}

// EmbeddingsBundle 打包所有用户的 embedding（embed 阶段输出）。
//
// 内容寻址复用：SectionHashes 以 "user_id|section" 为键记录内容
// hash；Embed 阶段只重算 hash 变化的分节，其余逐字节复用
// （spec/05-boundaries.md §6）。EmbeddingModel 不同时整个 bundle
// 不复用（模型切换 = 全量重算）。
type EmbeddingsBundle struct {
	UserIDs        []UserID                   // 长度 N
	SectionNames   []SectionName              // 长度 S（全局分节名集合）
	Embeddings     EmbeddingTensor            // [N][S][D]
	Hyde           map[SectionName][][]Vector // {section: [N][K][D]}，K=n_descriptors
	EmbeddingModel string
	Dim            int
	SectionHashes  map[string]string // {"user_id|section": hash}
	HydeHashes     map[string]string // {"user_id|section": hyde 内容 hash}
	UserTimestamps map[string]string // {"user_id": last_updated_at}
}

// N 返回用户数。
func (b *EmbeddingsBundle) N() int { return len(b.UserIDs) }

// S 返回分节数。
func (b *EmbeddingsBundle) S() int { return len(b.SectionNames) }

// Subset 取子集——query(1×M) 与 batch(M×N) 模式的基础原语。
// 未知 userID 返回错误（调用方 bug，静默跳过会破坏位置对齐）。
func (b *EmbeddingsBundle) Subset(userIDs []UserID) (*EmbeddingsBundle, error) {
	idx := make([]int, len(userIDs))
	pos := make(map[UserID]int, len(b.UserIDs))
	for i, uid := range b.UserIDs {
		pos[uid] = i
	}
	for k, uid := range userIDs {
		i, ok := pos[uid]
		if !ok {
			return nil, &ContractError{Field: "user_ids", Reason: "unknown user: " + string(uid)}
		}
		idx[k] = i
	}
	sub := &EmbeddingsBundle{
		UserIDs:        make([]UserID, len(userIDs)),
		SectionNames:   b.SectionNames,
		Embeddings:     make(EmbeddingTensor, len(userIDs)),
		Hyde:           map[SectionName][][]Vector{},
		EmbeddingModel: b.EmbeddingModel,
		Dim:            b.Dim,
		SectionHashes:  b.SectionHashes,
		HydeHashes:     b.HydeHashes,
		UserTimestamps: b.UserTimestamps,
	}
	for k, i := range idx {
		sub.UserIDs[k] = b.UserIDs[i]
		sub.Embeddings[k] = b.Embeddings[i]
	}
	for name, perUser := range b.Hyde {
		sliced := make([][]Vector, len(idx))
		for k, i := range idx {
			sliced[k] = perUser[i]
		}
		sub.Hyde[name] = sliced
	}
	return sub, nil
}

// ToMap 与 Python EmbeddingsBundle.to_dict 逐字段一致
// （张量本体不进 dict——Python 侧同此约定，走 npz 序列化）。
func (b *EmbeddingsBundle) ToMap() map[string]any {
	uids := make([]any, len(b.UserIDs))
	for i, u := range b.UserIDs {
		uids[i] = string(u)
	}
	names := make([]any, len(b.SectionNames))
	for i, n := range b.SectionNames {
		names[i] = string(n)
	}
	return map[string]any{
		"user_ids":        uids,
		"section_names":   names,
		"embedding_model": b.EmbeddingModel,
		"dim":             b.Dim,
		"section_hashes":  b.SectionHashes,
		"hyde_hashes":     b.HydeHashes,
		"user_timestamps": b.UserTimestamps,
	}
}

// SimilarityResult 是召回层输出：方向性相似度矩阵（similarity 阶段）。
//
// DirMatrix 是方向性分数（source→target，不做盲目对称化）；
// FusedMatrix 是跨分节融合分数。缺失分节 = 中性（mask + 分母修正），
// 不是零（spec/05-boundaries.md §1, §2）。
type SimilarityResult struct {
	SourceIDs   []UserID
	TargetIDs   []UserID
	DirMatrix   Matrix // [M, N]
	FusedMatrix Matrix // [M, N]
}

// IsSquare 判定 source/target 是否同一集合且同序（N×N 模式）。
// 对应 Python 的 is_square property：矩形模式（query×batch）为 false。
func (s *SimilarityResult) IsSquare() bool {
	if len(s.SourceIDs) != len(s.TargetIDs) {
		return false
	}
	for i := range s.SourceIDs {
		if s.SourceIDs[i] != s.TargetIDs[i] {
			return false
		}
	}
	return true
}
