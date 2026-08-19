package domain

import "sort"

// UserID 标识一个用户/实体画像。强类型：防止把任意字符串
// 误用作 ID（对应 Python 基线的 profile.id）。
type UserID string

// SectionName 是画像分节键（"skills" / "vision" / "project" / "needs"）。
type SectionName string

// PairID 是配对规范标识 "{min}__{max}"（与参数顺序无关）。
type PairID string

// StablePairID 返回与参数顺序无关的稳定 pair_id。
//
// Python 基线：sorted([u1, u2]) 后以 "__" 连接。按字典序排序保证
// 任意调用顺序得到同一 ID——novelty 排除与 match_history 都依赖
// 这一稳定语义（spec/05-boundaries.md §8）。
func StablePairID(a, b UserID) PairID {
	users := []string{string(a), string(b)}
	sort.Strings(users)
	return PairID(users[0] + "__" + users[1])
}

// Sorted 返回按字典序排序的副本（不修改原切片）。
// 排序是确定性的：所有遍历用户集合的地方都应先排序再迭代，
// 保证输出与 Python 侧 sorted() 逐位一致。
func Sorted(ids []UserID) []UserID {
	out := make([]UserID, len(ids))
	copy(out, ids)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
