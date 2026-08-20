package holdout

// manifest_test.go 常驻 CI（不解锁也跑）的 holdout 完整性校验。
//
// holdout/ 是冻结件（docs/workplan-issue7.md §5.4）：本目录全部文件
// （manifest.json 自身除外）的 sha256 登记在 manifest.json，任何改动都会
// 让本测试变红。真正的强制力来自 CODEOWNERS + 人类批准（治理 issue
// Cloudbird-Software/.github#104）——本测试防的是"悄悄改"。
//
// 重生成 manifest（仅限人类 owner，且必须留 PR 批准记录）：
//
//	MUTUAL_HOLDOUT_WRITE=1 go test ./holdout/ -run TestManifest
//
// 重生成后本测试会以 FAIL 结束一次（强制你再跑一遍确认绿）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

type manifest struct {
	Version   int               `json:"version"`
	Policy    string            `json:"policy"`
	Author    string            `json:"author"`
	UpdatedAt string            `json:"updated_at"`
	Files     map[string]string `json:"files"`
}

func hashHoldoutFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := filepath.ToSlash(path)
		if info.IsDir() || name == "manifest.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[name] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestManifest(t *testing.T) {
	current := hashHoldoutFiles(t)

	if os.Getenv("MUTUAL_HOLDOUT_WRITE") == "1" {
		m := manifest{
			Version: 1,
			Policy:  "holdout/ 冻结件；改动需人类 owner 批准（CODEOWNERS，.github#104）",
			Author:  "holdout-author (non-implementer)",
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Files:   current,
		}
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("manifest.json", append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("manifest 已重生成（%d 个文件）——此操作必须有人类批准记录；请重跑确认绿", len(current))
	}

	data, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("manifest.json 缺失: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest.json 解析失败: %v", err)
	}

	failed := false
	names := make([]string, 0, len(current))
	for name := range current {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		recorded, ok := m.Files[name]
		if !ok {
			t.Errorf("新增未登记文件 %s", name)
			failed = true
		} else if recorded != current[name] {
			t.Errorf("文件被改动 %s", name)
			failed = true
		}
	}
	for name := range m.Files {
		if _, ok := current[name]; !ok {
			t.Errorf("登记文件缺失 %s", name)
			failed = true
		}
	}
	if failed {
		t.Fatal("holdout manifest 校验失败")
	}
	fmt.Printf("holdout manifest OK（%d 个文件）\n", len(current))
}
