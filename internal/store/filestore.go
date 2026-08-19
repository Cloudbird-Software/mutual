package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

const (
	bundleFilename = "bundle.json"
	sectionsSubdir = "sections"
)

// FileStore 是基于文件系统的 Store 实现。
type FileStore struct {
	// Root 是存储根目录。
	Root string
	// NoveltyWindowMonths 是 novelty 排除的回看窗口（月）。
	NoveltyWindowMonths int
}

// NewFileStore 构造 FileStore 并建齐五个子目录（目录结构在任何
// IO 之前就可断言——目录布局是契约的一部分）。
func NewFileStore(root string, noveltyWindowMonths int) (*FileStore, error) {
	if noveltyWindowMonths <= 0 {
		noveltyWindowMonths = 6
	}
	fs := &FileStore{Root: root, NoveltyWindowMonths: noveltyWindowMonths}
	for _, dir := range []string{"raw", "processed", "embeds", "outputs", "cache"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return nil, fmt.Errorf("初始化存储目录 %s: %w", dir, err)
		}
	}
	return fs, nil
}

// sectionsDir 返回 processed/sections 路径。
func (fs *FileStore) sectionsDir() string {
	return filepath.Join(fs.Root, "processed", sectionsSubdir)
}

// GetSections 实现 Store：按 id 逐文件读取；不安全 ID 跳过
// （读侧 fail-soft，绝不含路径拼接）。
func (fs *FileStore) GetSections(userIDs []domain.UserID) (map[domain.UserID]domain.ExtractedSections, error) {
	dir := fs.sectionsDir()
	var files []string
	if userIDs == nil {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return map[domain.UserID]domain.ExtractedSections{}, nil
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				files = append(files, e.Name())
			}
		}
		sort.Strings(files)
	} else {
		for _, uid := range userIDs {
			if SafeFilename(string(uid)) {
				files = append(files, string(uid)+".json")
			}
		}
	}

	out := map[domain.UserID]domain.ExtractedSections{}
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue // 缺失 id 不是错误（读到什么给什么）
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			continue
		}
		es, err := extractedFromMap(doc)
		if err != nil {
			continue
		}
		out[es.ID] = es
	}
	return out, nil
}

// PutSections 实现 Store：失败提取（全占位）跳过；不安全 ID fail-loud
// （写侧拒绝，不允许静默改写 sections 目录之外的文件）。
func (fs *FileStore) PutSections(extracted []domain.ExtractedSections) error {
	if err := os.MkdirAll(fs.sectionsDir(), 0o755); err != nil {
		return err
	}
	for _, item := range extracted {
		if isFailedExtraction(item) {
			continue
		}
		if !SafeFilename(string(item.ID)) {
			return fmt.Errorf(
				"拒绝持久化不安全的 profile id %q：ID 只允许字母数字与 ._- 且不得以点开头（路径穿越守卫）",
				string(item.ID),
			)
		}
		data, err := json.MarshalIndent(item.ToMap(), "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(fs.sectionsDir(), string(item.ID)+".json"), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// GetEmbeddings 实现 Store：读取 embeds/bundle.json。
func (fs *FileStore) GetEmbeddings() (*domain.EmbeddingsBundle, error) {
	path := filepath.Join(fs.Root, "embeds", bundleFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return bundleFromJSON(data)
}

// PutEmbeddings 实现 Store：全尺寸写入 embeds/bundle.json。
func (fs *FileStore) PutEmbeddings(bundle *domain.EmbeddingsBundle) error {
	dir := filepath.Join(fs.Root, "embeds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := bundleToJSON(bundle)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, bundleFilename), data, 0o644)
}

// GetMatchHistory 实现 Store：按 novelty 窗口过滤。
//
// matched_at 缺失/不可解析的记录保守保留——novelty 排除是安全侧
// 特性，宁可多排除也不放松窗口（qodo #10 语义）。
func (fs *FileStore) GetMatchHistory() ([]MatchRecord, error) {
	path := filepath.Join(fs.Root, "match_history.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	now := time.Now().UTC()
	cutoff := monthsBefore(now, fs.NoveltyWindowMonths)

	var records []MatchRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec MatchRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // 坏行无法提取 pair_id，只能跳过
		}
		// matched_at 缺失/不可解析 → 保守保留（宁可多排除也不放松窗口）。
		ts, ok := parseTimestamp(rec.MatchedAt)
		if !ok || !ts.Before(cutoff) {
			records = append(records, rec)
		}
	}
	return records, nil
}

// PutMatches 实现 Store：append 匹配边到 match_history.jsonl。
func (fs *FileStore) PutMatches(edges []domain.Edge) error {
	if err := os.MkdirAll(fs.Root, 0o755); err != nil {
		return err
	}
	matchedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000000-07:00")
	path := filepath.Join(fs.Root, "match_history.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, edge := range edges {
		line, err := json.Marshal(MatchRecord{
			PairID:    edge.PairID,
			User1:     edge.User1,
			User2:     edge.User2,
			MatchedAt: matchedAt,
		}.ToMap())
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// bundle JSON 序列化（adapter 层格式，非核心契约）
// ---------------------------------------------------------------------------

// bundleJSON 是 bundle.json 的磁盘形状。
type bundleJSON struct {
	UserIDs        []string                 `json:"user_ids"`
	SectionNames   []string                 `json:"section_names"`
	EmbeddingModel string                   `json:"embedding_model"`
	Dim            int                      `json:"dim"`
	SectionHashes  map[string]string        `json:"section_hashes"`
	HydeHashes     map[string]string        `json:"hyde_hashes"`
	UserTimestamps map[string]string        `json:"user_timestamps"`
	Embeddings     [][][]float64            `json:"embeddings"`
	Hyde           map[string][][][]float64 `json:"hyde"`
}

func bundleToJSON(b *domain.EmbeddingsBundle) ([]byte, error) {
	doc := bundleJSON{
		UserIDs:        make([]string, len(b.UserIDs)),
		SectionNames:   make([]string, len(b.SectionNames)),
		EmbeddingModel: b.EmbeddingModel,
		Dim:            b.Dim,
		SectionHashes:  b.SectionHashes,
		HydeHashes:     b.HydeHashes,
		UserTimestamps: b.UserTimestamps,
		Embeddings:     make([][][]float64, len(b.Embeddings)),
		Hyde:           map[string][][][]float64{},
	}
	for i, uid := range b.UserIDs {
		doc.UserIDs[i] = string(uid)
	}
	for i, name := range b.SectionNames {
		doc.SectionNames[i] = string(name)
	}
	// [N][S][D]：每个 cell 的 SectionEmbeddings 恰含 1 条向量
	//（embed 阶段的构造保证；HyDE 描述向量走 Hyde 字段）。
	for i, user := range b.Embeddings {
		doc.Embeddings[i] = make([][]float64, len(user))
		for k, sec := range user {
			if len(sec) > 0 {
				doc.Embeddings[i][k] = sec[0]
			} else {
				doc.Embeddings[i][k] = []float64{}
			}
		}
	}
	for name, perUser := range b.Hyde {
		arr := make([][][]float64, len(perUser))
		for i, row := range perUser {
			arr[i] = make([][]float64, len(row))
			for k, vec := range row {
				arr[i][k] = vec
			}
		}
		doc.Hyde[string(name)] = arr
	}
	return json.MarshalIndent(doc, "", "  ")
}

func bundleFromJSON(data []byte) (*domain.EmbeddingsBundle, error) {
	var doc bundleJSON
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("bundle.json 解析失败: %w", err)
	}
	b := &domain.EmbeddingsBundle{
		UserIDs:        make([]domain.UserID, len(doc.UserIDs)),
		SectionNames:   make([]domain.SectionName, len(doc.SectionNames)),
		EmbeddingModel: doc.EmbeddingModel,
		Dim:            doc.Dim,
		SectionHashes:  doc.SectionHashes,
		HydeHashes:     doc.HydeHashes,
		UserTimestamps: doc.UserTimestamps,
		Embeddings:     make(domain.EmbeddingTensor, len(doc.Embeddings)),
		Hyde:           map[domain.SectionName][][]domain.Vector{},
	}
	for i, uid := range doc.UserIDs {
		b.UserIDs[i] = domain.UserID(uid)
	}
	for i, name := range doc.SectionNames {
		b.SectionNames[i] = domain.SectionName(name)
	}
	for i, user := range doc.Embeddings {
		b.Embeddings[i] = make(domain.UserEmbeddings, len(user))
		for k, vec := range user {
			b.Embeddings[i][k] = domain.SectionEmbeddings{domain.Vector(vec)}
		}
	}
	for name, perUser := range doc.Hyde {
		arr := make([][]domain.Vector, len(perUser))
		for i, row := range perUser {
			arr[i] = make([]domain.Vector, len(row))
			for k, vec := range row {
				arr[i][k] = domain.Vector(vec)
			}
		}
		b.Hyde[domain.SectionName(name)] = arr
	}
	return b, nil
}

// extractedFromMap 从 JSON 形状构造 ExtractedSections（hash 缺省重算）。
func extractedFromMap(d map[string]any) (domain.ExtractedSections, error) {
	id, _ := d["id"].(string)
	if id == "" {
		return domain.ExtractedSections{}, fmt.Errorf("sections 条目缺 id")
	}
	sections := map[domain.SectionName]string{}
	if raw, ok := d["sections"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				sections[domain.SectionName(k)] = s
			}
		}
	}
	hash, _ := d["hash"].(string)
	return domain.NewExtractedSections(domain.UserID(id), sections, hash), nil
}

// isFailedExtraction 是 §4 的 store 层判定：全部 section 均为
// "Not specified"（或无 section）。
func isFailedExtraction(item domain.ExtractedSections) bool {
	if len(item.Sections) == 0 {
		return true
	}
	for _, v := range item.Sections {
		if strings.TrimSpace(v) != "Not specified" {
			return false
		}
	}
	return true
}

// parseTimestamp 解析 ISO-8601 时间戳；naive 视为 UTC，
// 不可解析返回 (zero, false)。
func parseTimestamp(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	normalized := strings.ReplaceAll(raw, "Z", "+00:00")
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000000-07:00",
		"2006-01-02T15:04:05",
	} {
		if ts, err := time.Parse(layout, normalized); err == nil {
			if ts.Location() == time.UTC {
				return ts, true
			}
			return ts.UTC(), true
		}
	}
	return time.Time{}, false
}

// monthsBefore 精确回退 months 个月的日历算术（同日不存在时取当月
// 最后一天，如 3月31日 回退 1 个月 → 2月28/29日）。
func monthsBefore(now time.Time, months int) time.Time {
	month := int(now.Month()) - months
	year := now.Year()
	for month <= 0 {
		month += 12
		year--
	}
	day := now.Day()
	if last := lastDayOfMonth(year, month); day > last {
		day = last
	}
	return time.Date(year, time.Month(month), day, now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), now.Location())
}

func lastDayOfMonth(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
