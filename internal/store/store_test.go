package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// TestSafeFilename 路径穿越守卫（spec/05-boundaries.md §5 安全边界）：
// 只允许单段、字母数字开头、仅字母数字与 ._-。
func TestSafeFilename(t *testing.T) {
	safe := []string{"alice", "user-1", "a.b_c", "U2", "x9._-"}
	for _, id := range safe {
		if !SafeFilename(id) {
			t.Errorf("%q 应为安全 ID", id)
		}
	}
	unsafe := []string{
		"", "..", "../etc/passwd", "a/b", `a\b`, ".hidden",
		"a..b", "-leading-dash", "a b", "a;b", "中国",
	}
	for _, id := range unsafe {
		if SafeFilename(id) {
			t.Errorf("%q 应为不安全 ID", id)
		}
	}
}

func mustStore(t *testing.T, root string) *FileStore {
	t.Helper()
	fs, err := NewFileStore(root, 6)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return fs
}

// TestFileStoreInit 目录布局在任何 IO 之前就可断言（目录结构是契约的一部分）。
func TestFileStoreInit(t *testing.T) {
	root := t.TempDir()
	_ = mustStore(t, root)
	for _, dir := range []string{"raw", "processed", "embeds", "outputs", "cache"} {
		if st, err := os.Stat(filepath.Join(root, dir)); err != nil || !st.IsDir() {
			t.Errorf("目录 %s 未创建", dir)
		}
	}
}

// TestSectionsRoundTrip Put → Get 逐字段一致（hash 缺省重算）。
func TestSectionsRoundTrip(t *testing.T) {
	fs := mustStore(t, t.TempDir())
	sections := []domain.ExtractedSections{
		domain.NewExtractedSections("alice", map[domain.SectionName]string{
			"skills": "go", "needs": "reviewers",
		}, ""),
		domain.NewExtractedSections("bob", map[domain.SectionName]string{
			"skills": "python",
		}, ""),
	}
	if err := fs.PutSections(sections); err != nil {
		t.Fatalf("PutSections: %v", err)
	}
	got, err := fs.GetSections([]domain.UserID{"alice", "bob", "ghost"})
	if err != nil {
		t.Fatalf("GetSections: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("读到 %d 条（ghost 应缺失且非错误）", len(got))
	}
	if got["alice"].Sections["skills"] != "go" {
		t.Errorf("alice skills: got %v", got["alice"].Sections["skills"])
	}
	if got["alice"].Hash == "" {
		t.Error("hash 缺省时应重算")
	}

	// nil userIDs = 全量。
	all, err := fs.GetSections(nil)
	if err != nil || len(all) != 2 {
		t.Errorf("GetSections(nil): got %d err=%v", len(all), err)
	}
}

// TestPutSectionsFailedExtraction 全占位（"Not specified"）的失败提取
// 不落盘（spec/05-boundaries.md §4：否则永远不会重试）。
func TestPutSectionsFailedExtraction(t *testing.T) {
	fs := mustStore(t, t.TempDir())
	failed := domain.NewExtractedSections("ghost", map[domain.SectionName]string{
		"skills": "Not specified", "needs": "Not specified",
	}, "")
	ok := domain.NewExtractedSections("alice", map[domain.SectionName]string{
		"skills": "go",
	}, "")
	if err := fs.PutSections([]domain.ExtractedSections{failed, ok}); err != nil {
		t.Fatalf("PutSections: %v", err)
	}
	got, _ := fs.GetSections(nil)
	if _, exists := got["ghost"]; exists {
		t.Error("失败提取不应落盘")
	}
	if _, exists := got["alice"]; !exists {
		t.Error("正常提取应落盘")
	}
}

// TestPutSectionsUnsafeID 写侧 fail-loud：不安全 ID 拒绝持久化
// （不允许静默改写 sections 目录之外的文件）。
func TestPutSectionsUnsafeID(t *testing.T) {
	fs := mustStore(t, t.TempDir())
	bad := domain.NewExtractedSections("../escape", map[domain.SectionName]string{
		"skills": "x",
	}, "")
	if err := fs.PutSections([]domain.ExtractedSections{bad}); err == nil {
		t.Fatal("不安全 ID 应报错（路径穿越守卫）")
	}
	// 目录外无文件产生。
	if _, err := os.Stat(filepath.Join(fs.Root, "escape.json")); !os.IsNotExist(err) {
		t.Error("不应在 sections 目录外产生文件")
	}
}

// TestBundleRoundTrip Put → Get 逐位一致（[N][S][D] 张量 + hyde 形状）。
func TestBundleRoundTrip(t *testing.T) {
	fs := mustStore(t, t.TempDir())
	bundle := &domain.EmbeddingsBundle{
		UserIDs:        []domain.UserID{"alice", "bob"},
		SectionNames:   []domain.SectionName{"needs", "skills"},
		EmbeddingModel: "golden-embedder",
		Dim:            2,
		Embeddings: domain.EmbeddingTensor{
			{
				domain.SectionEmbeddings{domain.Vector{0.1, 0.2}},
				domain.SectionEmbeddings{domain.Vector{0.3, 0.4}},
			},
			{
				domain.SectionEmbeddings{domain.Vector{0.5, 0.6}},
				domain.SectionEmbeddings{domain.Vector{0.7, 0.8}},
			},
		},
		Hyde: map[domain.SectionName][][]domain.Vector{
			"skills": {nil, nil},
		},
	}
	if err := fs.PutEmbeddings(bundle); err != nil {
		t.Fatalf("PutEmbeddings: %v", err)
	}
	got, err := fs.GetEmbeddings()
	if err != nil || got == nil {
		t.Fatalf("GetEmbeddings: %v %v", got, err)
	}
	if len(got.UserIDs) != 2 || got.UserIDs[1] != "bob" {
		t.Errorf("user_ids: got %v", got.UserIDs)
	}
	if got.EmbeddingModel != "golden-embedder" || got.Dim != 2 {
		t.Errorf("模型元数据: got %s dim=%d", got.EmbeddingModel, got.Dim)
	}
	for i := range bundle.Embeddings {
		for k := range bundle.Embeddings[i] {
			for d := range bundle.Embeddings[i][k][0] {
				if got.Embeddings[i][k][0][d] != bundle.Embeddings[i][k][0][d] {
					t.Errorf("embeddings[%d][%d][%d]: got %v want %v",
						i, k, d, got.Embeddings[i][k][0][d], bundle.Embeddings[i][k][0][d])
				}
			}
		}
	}
}

// TestGetEmbeddingsMissing 不存在返回 (nil, nil)（embed 复用路径的空集语义）。
func TestGetEmbeddingsMissing(t *testing.T) {
	fs := mustStore(t, t.TempDir())
	got, err := fs.GetEmbeddings()
	if got != nil || err != nil {
		t.Errorf("缺失 bundle: got %v err=%v（应 nil, nil）", got, err)
	}
}

// TestMatchHistoryAppendOnly PutMatches 追加写入（不覆盖既有历史）。
func TestMatchHistoryAppendOnly(t *testing.T) {
	fs := mustStore(t, t.TempDir())
	edges1 := []domain.Edge{{User1: "alice", User2: "bob", PairID: "alice__bob"}}
	edges2 := []domain.Edge{{User1: "carol", User2: "david", PairID: "carol__david"}}
	if err := fs.PutMatches(edges1); err != nil {
		t.Fatalf("PutMatches #1: %v", err)
	}
	if err := fs.PutMatches(edges2); err != nil {
		t.Fatalf("PutMatches #2: %v", err)
	}
	history, err := fs.GetMatchHistory()
	if err != nil {
		t.Fatalf("GetMatchHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("历史应 append-only: got %d 条", len(history))
	}
	if history[0].PairID != "alice__bob" || history[1].PairID != "carol__david" {
		t.Errorf("历史顺序: got %v", history)
	}
}

// TestMatchHistoryNoveltyWindow 窗口外记录被过滤；matched_at 缺失/不可解析
// 保守保留（宁可多排除也不放松窗口，qodo #10）。
func TestMatchHistoryNoveltyWindow(t *testing.T) {
	fs := mustStore(t, t.TempDir())
	old := time.Now().UTC().AddDate(-2, 0, 0).Format("2006-01-02T15:04:05Z")
	recent := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	lines := []string{
		`{"pair_id": "old__pair", "user1": "a", "user2": "b", "matched_at": "` + old + `"}`,
		`{"pair_id": "recent__pair", "user1": "c", "user2": "d", "matched_at": "` + recent + `"}`,
		`{"pair_id": "no_ts__pair", "user1": "e", "user2": "f"}`,
		`{"pair_id": "bad_ts__pair", "user1": "g", "user2": "h", "matched_at": "not-a-date"}`,
	}
	path := filepath.Join(fs.Root, "match_history.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("预置历史: %v", err)
	}
	history, err := fs.GetMatchHistory()
	if err != nil {
		t.Fatalf("GetMatchHistory: %v", err)
	}
	got := map[string]bool{}
	for _, rec := range history {
		got[string(rec.PairID)] = true
	}
	if got["old__pair"] {
		t.Error("两年前的记录应在 novelty 窗口（6 个月）外被过滤")
	}
	for _, pid := range []string{"recent__pair", "no_ts__pair", "bad_ts__pair"} {
		if !got[pid] {
			t.Errorf("%s 应保守保留（窗口安全侧）", pid)
		}
	}
}

// TestGetMatchHistoryMissing 历史不存在返回空（首跑语义）。
func TestGetMatchHistoryMissing(t *testing.T) {
	fs := mustStore(t, t.TempDir())
	history, err := fs.GetMatchHistory()
	if err != nil || len(history) != 0 {
		t.Errorf("缺失历史: got %v err=%v", history, err)
	}
}
