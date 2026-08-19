package engine

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// Embedder 是 engine 对 embedding 服务的最小依赖（consumer-defined
// interface，与 LLMClient 同构）：texts 与返回向量一一对应。
type Embedder interface {
	// Embed 对一组文本生成向量 [N][D]。len(result) 必须等于
	// len(texts)，且各行维度一致——违反即实现 bug，调用方 fail loud。
	Embed(texts []string) [][]float64
}

// EmbedSections 生成 section + HyDE 向量（embed 阶段，纯变换；
// embedder 注入，无网络 IO）。
//
// 复用边界（spec/05-boundaries.md §6）：
//   - 复用是 content-addressed（SectionHashes 按 "user|section" 记录
//     内容 hash），不是 roster-addressed——改一个画像只重嵌该画像的
//     变化 cell，不影响其他人；
//   - existing.EmbeddingModel 与 model 不同 → existing 整体忽略
//     （迁移守卫）；同名 model 但维度变化 → 同样整体忽略，全量重嵌；
//   - 全尺寸向量始终存储；MRL 截断在计算副本上做（TruncateVectors）。
//
// 缺失 cell（NotSpecified / 空内容）为零向量，不产生嵌入调用。
func EmbedSections(
	sections []domain.ExtractedSections,
	hyde map[domain.UserID]domain.HydeDescriptors,
	model string,
	existing *domain.EmbeddingsBundle,
	embedder Embedder,
) (*domain.EmbeddingsBundle, error) {
	if embedder == nil {
		return nil, fmt.Errorf("embedder 未注入：EmbedSections 需要 Embedder 实现")
	}

	userIDs := make([]domain.UserID, len(sections))
	nameSet := map[string]bool{}
	for i, es := range sections {
		userIDs[i] = es.ID
		for name := range es.Sections {
			nameSet[string(name)] = true
		}
	}
	sectionNames := make([]string, 0, len(nameSet))
	for name := range nameSet {
		sectionNames = append(sectionNames, name)
	}
	sort.Strings(sectionNames)
	sectionIndex := make(map[string]int, len(sectionNames))
	for k, name := range sectionNames {
		sectionIndex[name] = k
	}

	reuse := existing != nil && existing.EmbeddingModel == model

	basePlan, hydePlan, texts := planCells(sections, hyde, existing, reuse, sectionIndex)
	vecs, dim, err := embedTexts(embedder, texts, existing, reuse)
	if err != nil {
		return nil, err
	}

	// 迁移守卫：同名 model 但维度变化 → existing 整体忽略，全量重嵌。
	if reuse && existing != nil && vecs != nil && dim != existing.Dim {
		reuse = false
		basePlan, hydePlan, texts = planCells(sections, hyde, nil, false, sectionIndex)
		vecs, dim, err = embedTexts(embedder, texts, existing, false)
		if err != nil {
			return nil, err
		}
	}

	nUsers := len(userIDs)
	nSections := len(sectionNames)

	embeddings := make(domain.EmbeddingTensor, nUsers)
	for i := range embeddings {
		embeddings[i] = make(domain.UserEmbeddings, nSections)
		for k := range embeddings[i] {
			embeddings[i][k] = domain.SectionEmbeddings{make(domain.Vector, dim)}
		}
	}

	sectionHashes := map[string]string{}
	for key, cell := range basePlan {
		if cell == nil {
			continue // 缺失 cell：零向量，不记 hash
		}
		var vec domain.Vector
		if cell.reuse {
			vec = copyVector(existing.Embeddings[cell.oldUser][cell.oldSection][0])
		} else {
			vec = copyVector(domain.Vector(vecs[cell.textPos]))
		}
		embeddings[key.user][sectionIndex[key.section]] = domain.SectionEmbeddings{vec}
		sectionHashes[string(userIDs[key.user])+"|"+key.section] = cell.hash
	}

	// 每个 section 的描述符槽位数 = 全体用户的最大值（不足补零向量）。
	nDesc := make(map[string]int, len(sectionNames))
	for _, name := range sectionNames {
		nDesc[name] = 0
	}
	for _, hd := range hyde {
		for name, descs := range hd.Descriptors {
			if _, known := nDesc[string(name)]; known && len(descs) > nDesc[string(name)] {
				nDesc[string(name)] = len(descs)
			}
		}
	}
	hydeArrays := make(map[domain.SectionName][][]domain.Vector, len(nDesc))
	hydeHashes := map[string]string{}
	for name, count := range nDesc {
		arr := make([][]domain.Vector, nUsers)
		for i := range arr {
			row := make([]domain.Vector, count)
			for k := range row {
				row[k] = make(domain.Vector, dim)
			}
			arr[i] = row
		}
		hydeArrays[domain.SectionName(name)] = arr
	}
	for key, cell := range hydePlan {
		var vec domain.Vector
		if cell.reuse {
			vec = copyVector(existing.Hyde[domain.SectionName(key.section)][cell.oldUser][cell.oldSection])
		} else {
			vec = copyVector(domain.Vector(vecs[cell.textPos]))
		}
		hydeArrays[domain.SectionName(key.section)][key.user][key.desc] = vec
		hydeHashes[string(userIDs[key.user])+"|"+key.section+"|"+itoa(key.desc)] = cell.hash
	}

	userTimestamps := map[string]string{}
	if reuse && existing != nil {
		for k, v := range existing.UserTimestamps {
			userTimestamps[k] = v
		}
	}

	return &domain.EmbeddingsBundle{
		UserIDs:        userIDs,
		SectionNames:   toSectionNames(sectionNames),
		Embeddings:     embeddings,
		Hyde:           hydeArrays,
		EmbeddingModel: model,
		Dim:            dim,
		SectionHashes:  sectionHashes,
		HydeHashes:     hydeHashes,
		UserTimestamps: userTimestamps,
	}, nil
}

// cellKey / hydeCellKey 是嵌入计划的最小定位单元。
type cellKey struct {
	user    int
	section string
}

type hydeCellKey struct {
	user    int
	section string
	desc    int
}

// embedCell 是一个待嵌入（或复用）的单元：
// reuse=true 时从 existing 的 (oldUser, oldSection) 拷贝；
// 否则取新嵌入结果的 textPos 行。hash 是该 cell 的内容 hash。
type embedCell struct {
	reuse      bool
	oldUser    int
	oldSection int
	textPos    int
	hash       string
}

// planCells 构建 cell 级嵌入计划。
//
// 返回 basePlan（分节 cell，值 nil = 缺失）、hydePlan（描述符 cell）、
// texts（待嵌入文本，顺序对应 textPos）。reuse=false 时 existing
// 视为不存在（整体忽略，迁移守卫路径）。
func planCells(
	sections []domain.ExtractedSections,
	hyde map[domain.UserID]domain.HydeDescriptors,
	existing *domain.EmbeddingsBundle,
	reuse bool,
	sectionIndex map[string]int,
) (map[cellKey]*embedCell, map[hydeCellKey]*embedCell, []string) {
	basePlan := map[cellKey]*embedCell{}
	hydePlan := map[hydeCellKey]*embedCell{}
	var texts []string

	oldUserIndex := map[domain.UserID]int{}
	oldSectionIndex := map[string]int{}
	if reuse && existing != nil {
		for k, uid := range existing.UserIDs {
			oldUserIndex[uid] = k
		}
		for k, name := range existing.SectionNames {
			oldSectionIndex[string(name)] = k
		}
	}

	for i, es := range sections {
		uid := es.ID
		for _, name := range sortedSectionKeys(sectionIndex) {
			content := es.Sections[domain.SectionName(name)]
			if content == "" || content == NotSpecified {
				basePlan[cellKey{i, name}] = nil
				continue
			}
			hash := domain.HashText(content)
			if reuse && existing != nil {
				if oldI, okU := oldUserIndex[uid]; okU {
					if oldK, okS := oldSectionIndex[name]; okS {
						if existing.SectionHashes[string(uid)+"|"+name] == hash {
							basePlan[cellKey{i, name}] = &embedCell{
								reuse: true, oldUser: oldI, oldSection: oldK, hash: hash,
							}
							continue
						}
					}
				}
			}
			basePlan[cellKey{i, name}] = &embedCell{textPos: len(texts), hash: hash}
			texts = append(texts, content)
		}
		hd, ok := hyde[uid]
		if !ok {
			continue
		}
		for _, name := range sortedDescriptorKeys(hd.Descriptors) {
			if _, known := sectionIndex[string(name)]; !known {
				continue
			}
			oldSlots := 0
			if reuse && existing != nil {
				if arr, has := existing.Hyde[name]; has && len(arr) > 0 {
					oldSlots = len(arr[0]) // 全体用户统一槽位数（构造保证）
				}
			}
			for k, desc := range hd.Descriptors[name] {
				hash := domain.HashText(desc)
				if reuse && existing != nil && k < oldSlots {
					if oldI, okU := oldUserIndex[uid]; okU {
						if existing.HydeHashes[string(uid)+"|"+string(name)+"|"+itoa(k)] == hash {
							hydePlan[hydeCellKey{i, string(name), k}] = &embedCell{
								reuse: true, oldUser: oldI, oldSection: k, hash: hash,
							}
							continue
						}
					}
				}
				hydePlan[hydeCellKey{i, string(name), k}] = &embedCell{textPos: len(texts), hash: hash}
				texts = append(texts, desc)
			}
		}
	}
	return basePlan, hydePlan, texts
}

// embedTexts 调 embedder 嵌入 texts；空 texts 返回 (nil, 继承维度)。
// 维度继承：texts 为空且复用时沿用 existing.Dim（无新向量可推断）。
func embedTexts(embedder Embedder, texts []string, existing *domain.EmbeddingsBundle, reuse bool) ([][]float64, int, error) {
	if len(texts) == 0 {
		if reuse && existing != nil {
			return nil, existing.Dim, nil
		}
		return nil, 0, nil
	}
	vecs := embedder.Embed(texts)
	if len(vecs) != len(texts) {
		return nil, 0, fmt.Errorf("embedder 契约违反：请求 %d 条文本，返回 %d 条向量", len(texts), len(vecs))
	}
	dim := 0
	if len(vecs) > 0 {
		dim = len(vecs[0])
		for _, row := range vecs {
			if len(row) != dim {
				return nil, 0, fmt.Errorf("embedder 契约违反：向量维度不一致（%d vs %d）", len(row), dim)
			}
		}
	}
	return vecs, dim, nil
}

func copyVector(v domain.Vector) domain.Vector {
	return append(domain.Vector(nil), v...)
}

func sortedSectionKeys(sectionIndex map[string]int) []string {
	names := make([]string, 0, len(sectionIndex))
	for name := range sectionIndex {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedDescriptorKeys(m map[domain.SectionName][]string) []domain.SectionName {
	names := make([]domain.SectionName, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

func toSectionNames(names []string) []domain.SectionName {
	out := make([]domain.SectionName, len(names))
	for i, n := range names {
		out[i] = domain.SectionName(n)
	}
	return out
}

// SupportsMRL 判定 embedding model 是否支持 Matryoshka 截断
// （spec 沉默 A-12：按 OpenAI text-embedding-3-* 家族前缀判定）。
func SupportsMRL(model string) bool {
	return strings.HasPrefix(model, "text-embedding-3")
}

// TruncateVectors MRL 截断：全尺寸向量截断到 dim 维并 L2 归一化
// （零向量保持为零）。截断只发生在计算副本上（§6：存储恒全尺寸）。
func TruncateVectors(vectors []domain.Vector, dim int) ([]domain.Vector, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("dim 必须为正整数，got %d", dim)
	}
	out := make([]domain.Vector, len(vectors))
	for i, v := range vectors {
		if dim > len(v) {
			out[i] = append(domain.Vector(nil), v...)
			continue
		}
		truncated := append(domain.Vector(nil), v[:dim]...)
		norm := 0.0
		for _, x := range truncated {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		if norm > eps {
			for k := range truncated {
				truncated[k] /= norm
			}
		}
		out[i] = truncated
	}
	return out, nil
}
