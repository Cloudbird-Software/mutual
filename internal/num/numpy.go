// Package num 复刻 NumPy 标量归约的浮点求和顺序。
//
// 目的：golden 差分测试要求 Go 侧与 Python 基线（NumPy）在归约结果上
// **逐位一致**。IEEE754 浮点加法不满足结合律，求和顺序不同结果可能差
// 1 ulp，因此必须复刻 NumPy 的成对求和（pairwise summation）策略：
//
//	n < 8          ：顺序求和；
//	8 ≤ n ≤ 128    ：8 路部分和 r[0..7] 展开，再按
//	                  ((r0+r1)+(r2+r3)) + ((r4+r5)+(r6+r7)) 合并；
//	n > 128        ：对半切（下半界对齐到 8 的倍数）递归后相加。
//
// 参考：numpy/core/src/umath/loops_utils.src（pairwise_sum_@TYPE@）。
// NumPy 的 add.reduce / np.linalg.norm（向量范数）均走此路径。
//
// 注意：np.dot（BLAS ddot）与 np.einsum 的求和顺序与此不同（BLAS 分块
// 向量化），且随后端实现变化；本包不试图复刻它们——下游 golden 断言
// 均在 ≥1e-4 舍入精度上比较，BLAS 求和的 ≤1 ulp 差异不会传导。
package num

import "math"

const pairwiseBlocksize = 128

// Sum 复刻 numpy add.reduce 的成对求和（逐位一致）。
func Sum(a []float64) float64 {
	n := len(a)
	if n < 8 {
		res := 0.0
		for i := 0; i < n; i++ {
			res += a[i]
		}
		return res
	}
	if n <= pairwiseBlocksize {
		var r [8]float64
		for i := 0; i < 8; i++ {
			r[i] = a[i]
		}
		for i := 8; i < n-(n%8); i += 8 {
			r[0] += a[i+0]
			r[1] += a[i+1]
			r[2] += a[i+2]
			r[3] += a[i+3]
			r[4] += a[i+4]
			r[5] += a[i+5]
			r[6] += a[i+6]
			r[7] += a[i+7]
		}
		res := ((r[0] + r[1]) + (r[2] + r[3])) + ((r[4] + r[5]) + (r[6] + r[7]))
		for i := n - (n % 8); i < n; i++ {
			res += a[i]
		}
		return res
	}
	n2 := n / 2
	n2 -= n2 % 8
	return Sum(a[:n2]) + Sum(a[n2:])
}

// Norm 复刻 np.linalg.norm(x)（2-范数）：sqrt(Sum(x*x))。
func Norm(a []float64) float64 {
	sq := make([]float64, len(a))
	for i, v := range a {
		sq[i] = v * v
	}
	return math.Sqrt(Sum(sq))
}
