package num

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"
)

// logVector 是 golden/rng/log_vectors.json 的一条采样：IEEE754 位模式。
type logVector struct {
	X   string `json:"x"`
	Log string `json:"log"`
}

// TestGlibcLogGolden 对 2.5 万个本机 libm（glibc 2.39）采样点逐位比对，
// 覆盖近 1 快路径、全部 128 个表区间、跨数量级随机值、(0,1) 密集域
// （randn 极坐标法 r2 落点）与非正规数/特殊值。
//
// 这是"Go 复刻的就是 glibc 那条计算路径"的直接证据：任何多项式系数、
// 表项或求值序的偏差都会在这里暴露为 1 ULP 失配。
func TestGlibcLogGolden(t *testing.T) {
	raw, err := os.ReadFile("../../golden/rng/log_vectors.json")
	if err != nil {
		t.Fatalf("读取 golden log 向量失败: %v", err)
	}
	var vectors []logVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("解析 golden log 向量失败: %v", err)
	}
	if len(vectors) < 20000 {
		t.Fatalf("golden 向量过少: %d", len(vectors))
	}

	mismatches := 0
	for i, v := range vectors {
		x, err1 := strconv.ParseUint(v.X, 0, 64)
		logWant, err2 := strconv.ParseUint(v.Log, 0, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("第 %d 条位模式解析失败: %v / %v", i, err1, err2)
		}
		got := GlibcLog(math.Float64frombits(x))
		want := math.Float64frombits(logWant)
		if math.Float64bits(got) != math.Float64bits(want) {
			// NaN 位模式可能不同（glibc 产生特定 quiet NaN），只要求 NaN-性一致。
			if !math.IsNaN(got) || !math.IsNaN(want) {
				mismatches++
				if mismatches <= 5 {
					t.Errorf("第 %d 条 x=%v（bits 0x%016x）: got %v want %v",
						i, math.Float64frombits(x), x, got, want)
				}
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d/%d 条 log 向量失配", mismatches, len(vectors))
	}
}
