package domain

// PrefMatrix 是双向偏好矩阵——匹配市场（match 阶段）的输入。
//
// 来源：PairScore 的方向性 LLM 分数（pre_matrix 阶段桥接）：
//   - PrefLeftToRight[i][j] = left_i 对 right_j 的偏好；
//   - PrefRightToLeft[j][i] = right_j 对 left_i 的偏好。
//
// 消费方：NSW 求解器（nswMaximize）与 envy 公平性检查。
// 方向性语义不可丢：两个矩阵不是转置关系（spec/01-schemas.md §8）。
type PrefMatrix struct {
	LeftIDs         []UserID
	RightIDs        []UserID
	PrefLeftToRight Matrix // [M, N]，M=len(LeftIDs), N=len(RightIDs)
	PrefRightToLeft Matrix // [N, M]
}

// M 返回左侧（member）规模。
func (pm *PrefMatrix) M() int { return len(pm.LeftIDs) }

// N 返回右侧（pool）规模。
func (pm *PrefMatrix) N() int { return len(pm.RightIDs) }

// NewPrefMatrix 构造零偏好矩阵（形状按 ID 列表推断）。
func NewPrefMatrix(leftIDs, rightIDs []UserID) *PrefMatrix {
	m, n := len(leftIDs), len(rightIDs)
	return &PrefMatrix{
		LeftIDs:         leftIDs,
		RightIDs:        rightIDs,
		PrefLeftToRight: NewMatrixZeros(m, n),
		PrefRightToLeft: NewMatrixZeros(n, m),
	}
}

// ToMap 与 Python PrefMatrix.to_dict 逐字段一致（矩阵转嵌套 list）。
func (pm *PrefMatrix) ToMap() map[string]any {
	left := make([]any, len(pm.LeftIDs))
	for i, u := range pm.LeftIDs {
		left[i] = string(u)
	}
	right := make([]any, len(pm.RightIDs))
	for i, u := range pm.RightIDs {
		right[i] = string(u)
	}
	return map[string]any{
		"left_ids":           left,
		"right_ids":          right,
		"pref_left_to_right": pm.PrefLeftToRight.ToPlain(),
		"pref_right_to_left": pm.PrefRightToLeft.ToPlain(),
	}
}
