package rng

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/num"
)

// Golden 差分测试（RNG-1 gate）：Go 移植与 Python 基线的 numpy
// RandomState 随机流逐位一致。golden/rng/reference.json 由 Python 基线
// 捕获（seeds: 0/1/101/202/999/12345 各 12 个 rand/randn 值 +
// RandomState(7).rand(2,3) + RandomState(12345).randn(4,4,8)）。

func loadGolden(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "golden", "rng", "reference.json"))
	if err != nil {
		t.Fatalf("读取 golden 参考失败: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("解析 golden 参考失败: %v", err)
	}
	return raw
}

func parseSeed(t *testing.T, s string) uint32 {
	t.Helper()
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		t.Fatalf("seed %q 解析失败: %v", s, err)
	}
	return uint32(v)
}

var goldenSeeds = []string{"0", "1", "101", "202", "999", "12345"}

// TestGoldenRand 验证 rand()（Float64）流与 numpy RandomState 逐位一致。
func TestGoldenRand(t *testing.T) {
	raw := loadGolden(t)
	for _, seed := range goldenSeeds {
		var want []float64
		if err := json.Unmarshal(raw["rand_"+seed], &want); err != nil {
			t.Fatalf("解析 rand_%s 失败: %v", seed, err)
		}
		rs := New(parseSeed(t, seed))
		for i, exp := range want {
			if got := rs.Float64(); got != exp {
				t.Fatalf("rand seed=%s 第 %d 个值: got %v want %v", seed, i, got, exp)
			}
		}
	}
}

// TestGoldenRandn 验证 randn()（NormFloat64，legacy_gauss）流与 numpy 逐位一致。
func TestGoldenRandn(t *testing.T) {
	raw := loadGolden(t)
	for _, seed := range goldenSeeds {
		var want []float64
		if err := json.Unmarshal(raw["randn_"+seed], &want); err != nil {
			t.Fatalf("解析 randn_%s 失败: %v", seed, err)
		}
		rs := New(parseSeed(t, seed))
		for i, exp := range want {
			if got := rs.NormFloat64(); got != exp {
				// randn 含 log/sqrt 浮点路径：同 IEEE754 运算序列下应逐位
				// 一致；若因 libm 实现差异出现末位偏差，在此显式暴露。
				t.Fatalf("randn seed=%s 第 %d 个值: got %v want %v", seed, i, got, exp)
			}
		}
	}
}

// TestGoldenRand2x3 验证 RandomState(7).rand(2,3) 的矩阵抽取顺序。
func TestGoldenRand2x3(t *testing.T) {
	raw := loadGolden(t)
	var want [][]float64
	if err := json.Unmarshal(raw["rand_7_2x3"], &want); err != nil {
		t.Fatalf("解析 rand_7_2x3 失败: %v", err)
	}
	got := New(7).Rand2(2, 3)
	for i := range want {
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("rand(2,3)[%d][%d]: got %v want %v", i, j, got[i][j], want[i][j])
			}
		}
	}
}

// TestGoldenEmbedder 验证 golden cohort 的确定性 embedder 底座
// （spec/04-fixtures.md §4）：RandomState(12345).randn(4,4,8) →
// base[...,0] += 5.0（公共正向轴，保证任意两用户 cosine > 0）→
// 沿最后一维 L2 归一化。捕获值是归一化后的最终向量。
func TestGoldenEmbedder(t *testing.T) {
	raw := loadGolden(t)
	var want [][][]float64
	if err := json.Unmarshal(raw["golden_embedder_4x4x8"], &want); err != nil {
		t.Fatalf("解析 golden_embedder_4x4x8 失败: %v", err)
	}
	got := New(12345).RandN3(4, 4, 8)
	for i := range got {
		for j := range got[i] {
			got[i][j][0] += 5.0
			norm := num.Norm(got[i][j])
			for k := range got[i][j] {
				got[i][j][k] /= norm
				if got[i][j][k] != want[i][j][k] {
					t.Fatalf("golden embedder[%d][%d][%d]: got %v want %v",
						i, j, k, got[i][j][k], want[i][j][k])
				}
			}
		}
	}
}
