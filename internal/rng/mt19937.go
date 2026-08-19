// Package rng 提供 NumPy RandomState 兼容的确定性随机流。
//
// 这是 Python 基线（numpy.random.RandomState）的逐位兼容移植，是整个
// 重写等价性论证的地基：golden 差分测试（internal/rng/mt19937_test.go 对
// golden/rng/reference.json 逐位比对）证明 Go 侧与 Python 基线产生
// **同一条随机流**，因此所有下游确定性算法（surrogate 噪声、合成市场、
// golden embedder）在两种实现下逐位一致。
//
// 兼容范围（与 Python 基线实际使用的调用面一一对应，不做多余泛化）：
//   - RandomState(seed)：标量 seed ∈ [0, 2^32-1]，init_genrand 播种；
//   - rand()：  rk_double——(a>>5)*2^26 + (b>>6) / 2^53（53 位均匀）；
//   - randn()： legacy_gauss——极坐标法（Marsaglia polar），带缓存值。
//
// 参考：numpy/random/src/distributions/distribution.c（rk_double）与
// numpy/random/src/legacy/legacy-distributions.c（legacy_gauss）。
package rng

import (
	"math"

	"github.com/Cloudbird-Software/mutual/internal/num"
)

const (
	n         = 624
	m         = 397
	matrixA   = 0x9908b0df
	upperMask = 0x80000000
	lowerMask = 0x7fffffff
)

// RandomState 是 NumPy legacy RandomState 的逐位兼容移植。
//
// 非并发安全：每个确定性流程持有自己的实例（与 Python 侧
// np.random.RandomState(seed) 的用法一致）。
type RandomState struct {
	mt       [n]uint32
	mti      int // index == n 表示需要 twist
	cached   float64
	hasGauss bool // legacy_gauss 的缓存值（randn 每两次消耗两个以上的 double）
}

// New 返回以 init_genrand(seed) 播种的 RandomState。
//
// seed 必须在 [0, 2^32-1]（与 numpy 对标量 seed 的约束一致；
// Python 侧调用面已用 % 2^32 归一化，见 fake embedder）。
func New(seed uint32) *RandomState {
	rs := &RandomState{mti: n}
	rs.mt[0] = seed
	for i := 1; i < n; i++ {
		rs.mt[i] = 1812433253*(rs.mt[i-1]^(rs.mt[i-1]>>30)) + uint32(i)
	}
	return rs
}

// nextUint32 返回下一个 tempering 后的 32 位输出（必要时先 twist）。
func (rs *RandomState) nextUint32() uint32 {
	if rs.mti >= n {
		rs.twist()
		rs.mti = 0
	}
	y := rs.mt[rs.mti]
	rs.mti++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	y ^= y >> 18
	return y
}

// twist 用 MT19937 的生成多项式刷新状态数组。
func (rs *RandomState) twist() {
	for i := 0; i < n; i++ {
		y := (rs.mt[i] & upperMask) | (rs.mt[(i+1)%n] & lowerMask)
		rs.mt[i] = rs.mt[(i+m)%n] ^ (y >> 1)
		if y&1 != 0 {
			rs.mt[i] ^= matrixA
		}
	}
}

// Float64 对应 numpy RandomState.rand() / random_sample()：
//
//	(a >> 5) * 2^26 + (b >> 6) 均匀落在 [0, 1) 的 2^53 网格上。
func (rs *RandomState) Float64() float64 {
	a := float64(rs.nextUint32() >> 5)
	b := float64(rs.nextUint32() >> 6)
	return (a*67108864.0 + b) / 9007199254740992.0
}

// NormFloat64 对应 numpy RandomState.randn()（legacy_gauss）：
// 极坐标法（Marsaglia polar），缓存 f*x1 供下一次调用返回。
func (rs *RandomState) NormFloat64() float64 {
	if rs.hasGauss {
		rs.hasGauss = false
		t := rs.cached
		rs.cached = 0
		return t
	}
	var x1, x2, r2, f float64
	for {
		x1 = 2.0*rs.Float64() - 1.0
		x2 = 2.0*rs.Float64() - 1.0
		r2 = x1*x1 + x2*x2
		if r2 < 1.0 && r2 != 0.0 {
			break
		}
	}
	// Polar method, a more efficient version of the Box-Muller approach.
	f = math.Sqrt(-2.0 * num.GlibcLog(r2) / r2)
	rs.cached = f * x1
	rs.hasGauss = true
	return f * x2
}

// RandN2 返回形状 [rows][cols] 的标准正态矩阵（row-major 抽取顺序），
// 对应 numpy RandomState.randn(rows, cols)。
func (rs *RandomState) RandN2(rows, cols int) [][]float64 {
	out := make([][]float64, rows)
	for i := range out {
		out[i] = make([]float64, cols)
		for j := range out[i] {
			out[i][j] = rs.NormFloat64()
		}
	}
	return out
}

// RandN3 返回形状 [d0][d1][d2] 的标准正态张量（row-major 抽取顺序），
// 对应 numpy RandomState.randn(d0, d1, d2)。
func (rs *RandomState) RandN3(d0, d1, d2 int) [][][]float64 {
	out := make([][][]float64, d0)
	for i := range out {
		out[i] = rs.RandN2(d1, d2)
	}
	return out
}

// Rand2 返回形状 [rows][cols] 的均匀 [0,1) 矩阵（row-major 抽取顺序），
// 对应 numpy RandomState.rand(rows, cols)。
func (rs *RandomState) Rand2(rows, cols int) [][]float64 {
	out := make([][]float64, rows)
	for i := range out {
		out[i] = make([]float64, cols)
		for j := range out[i] {
			out[i][j] = rs.Float64()
		}
	}
	return out
}
