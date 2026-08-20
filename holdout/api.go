// Package holdout 是 holdout 测试套件（冻结件）。
//
// 纪律（docs/workplan-issue7.md §5.4）：
//   - 实现/优化 agent 禁止阅读本包内容（README.md 除外）。
//   - 断言文件（*_test.go、scenarios/*.json）冻结，内容哈希登记在 manifest.json，
//     由 manifest_test.go 常驻 CI 校验；改动需人类 owner 批准。
//   - api.go 中的 Default 是唯一接线点：gate 时由人类 gate keeper 把 Harness
//     接到真实实现，只改接线，不改断言。
//   - 默认 go test 下本包功能测试全部 t.Skip；仅 gate 时以
//     MUTUAL_HOLDOUT=1 go test ./holdout/ 运行。manifest_test.go 不解锁也跑。
package holdout

import (
	"errors"
	"sort"
)

// ErrNotWired 表示 holdout 适配层尚未接到真实 harness 入口。
var ErrNotWired = errors.New(
	"holdout 适配层未接线：由人类 gate keeper 编辑 holdout/api.go 的 Default " +
		"完成接线；禁止修改断言文件")

// Harness 是 holdout 测试与未来 Harness 实现之间的唯一接口。
// gate keeper 接线时把 issue #7 的 Stage 0–8 端到端入口适配为本接口。
type Harness interface {
	// RunWorld 端到端运行：profile 文本（id → 原文）→ WorldResult。
	// 实现必须对 map 键排序后处理（本仓铁律：map 遍历有序）。
	RunWorld(profiles map[string]string) (WorldResult, error)
}

// WorldResult 是 RunWorld 的统一返回结构。
type WorldResult struct {
	Level      map[[2]string]int     // (focal, counterpart) -> acceptance_level 0-4
	Eligible   map[[2]string]bool    // (focal, counterpart) -> 资格
	Reason     map[[2]string]string  // (focal, counterpart) -> ineligible reason
	Confidence map[[2]string]float64 // (focal, counterpart) -> 置信度 [0,1]
	U          map[[2]string]float64 // (focal, counterpart) -> 估计定向效用 u_hat
	Matching   [][2]string           // 最终匹配，无序对，a < b（字符串序）
}

func pair(f, c string) [2]string { return [2]string{f, c} }

// LevelOf 返回 focal 对 counterpart 的 acceptance_level。
func (w WorldResult) LevelOf(focal, counterpart string) int {
	return w.Level[pair(focal, counterpart)]
}

// IsEligible 返回 focal 视角下 counterpart 的资格。
func (w WorldResult) IsEligible(focal, counterpart string) bool {
	return w.Eligible[pair(focal, counterpart)]
}

// ReasonOf 返回 ineligible reason（无则空串）。
func (w WorldResult) ReasonOf(focal, counterpart string) string {
	return w.Reason[pair(focal, counterpart)]
}

// ConfOf 返回置信度（缺省 1.0，即"实现未上报"按不惩罚处理）。
func (w WorldResult) ConfOf(focal, counterpart string) float64 {
	if v, ok := w.Confidence[pair(focal, counterpart)]; ok {
		return v
	}
	return 1.0
}

// UHat 返回估计定向效用。
func (w WorldResult) UHat(focal, counterpart string) float64 {
	return w.U[pair(focal, counterpart)]
}

// Degree 返回 agent 在最终匹配中的度。
func (w WorldResult) Degree(agent string) int {
	n := 0
	for _, e := range w.Matching {
		if e[0] == agent || e[1] == agent {
			n++
		}
	}
	return n
}

// IsMatched 返回两 agent 是否被匹配（与顺序无关）。
func (w WorldResult) IsMatched(x, y string) bool {
	a, b := x, y
	if a > b {
		a, b = b, a
	}
	for _, e := range w.Matching {
		if e[0] == a && e[1] == b {
			return true
		}
	}
	return false
}

// SortedIDs 返回排序后的 agent id 列表（供接线实现使用，保持遍历有序）。
func SortedIDs(profiles map[string]string) []string {
	ids := make([]string, 0, len(profiles))
	for id := range profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type notWired struct{}

func (notWired) RunWorld(map[string]string) (WorldResult, error) {
	return WorldResult{}, ErrNotWired
}

// Default 是全部 holdout 测试使用的 Harness 实例。
//
// ★ 接线点（gate keeper 唯一允许改的地方）：把 notWired{} 替换为真实实现。
var Default Harness = notWired{}
