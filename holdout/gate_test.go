package holdout

import (
	"os"
	"testing"
)

// requireUnlock 把测试门禁在 MUTUAL_HOLDOUT=1 之后。
// 默认 go test（含 make check / CI）下全部 skip——这是设计意图。
func requireUnlock(t *testing.T) {
	t.Helper()
	if os.Getenv("MUTUAL_HOLDOUT") != "1" {
		t.Skip("holdout 套件锁定；仅波次 gate 时由人类以 MUTUAL_HOLDOUT=1 运行")
	}
}

// runWorld 走唯一接线点；未接线时 fail 而不是 skip（silence is not consent）。
func runWorld(t *testing.T, profiles map[string]string) WorldResult {
	t.Helper()
	res, err := Default.RunWorld(profiles)
	if err != nil {
		t.Fatalf("holdout 运行失败: %v", err)
	}
	return res
}
