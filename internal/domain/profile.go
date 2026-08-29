package domain

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// MaxSectionLen 单个分节文本的长度上限（runes）。
//
// 画像分节是短自述（技能/需求/愿景/项目各一段）；无上限的分节会把
// 超大文本逐字带进该成员参与的每一个 LLM 打分 prompt 与 embedding
// 调用（实测 2MB 分节渲染成 2.96MB prompt）——面向运营方的财务
// DoS / 资源滥用面（红队 RT3 #50）。上限取宽松值：容纳任何真实的
// 长自述，同时把单成员的成本放大系数钉死在常数级。
const MaxSectionLen = 8000

// Profile 是用户/实体的原始自由文本画像（管线入口类型）。
//
// 对应 spec/01-schemas.md §1：sections 的键是分节名，值是自由文本；
// extract 阶段将其结构化，画像本身不被修改。
type Profile struct {
	ID            UserID
	Sections      map[SectionName]string
	LastUpdatedAt *string // 可选，ISO-8601 时间戳
}

// NewProfile 构造 Profile 并校验最小不变量：ID 非空、sections 非 nil、
// ID 不含会破坏下游 prompt 结构的字符（RT-2026-08 #34：ID 原样渲染进
// scoring 块头 "### Pair N: (u1, u2)" 与 intro 的 Person 行——换行/逗号/
// 括号可注入受信指令槽位或伪造批量块头，故在构造咽喉处拒绝）、
// 分节长度不超上限（RT3 #50 财务 DoS，见 MaxSectionLen）。
func NewProfile(id UserID, sections map[SectionName]string, lastUpdatedAt *string) Profile {
	for _, r := range id {
		if r < 0x20 || r == ',' || r == '(' || r == ')' {
			return Profile{}
		}
	}
	if _, over := OverlongSection(sections); over {
		return Profile{}
	}
	if sections == nil {
		sections = map[SectionName]string{}
	}
	return Profile{ID: id, Sections: sections, LastUpdatedAt: lastUpdatedAt}
}

// OverlongSection 返回首个超过 MaxSectionLen 的分节（按分节名排序，
// 确定性）。无超长分节返回 false。
func OverlongSection(sections map[SectionName]string) (SectionName, bool) {
	if len(sections) == 0 {
		return "", false
	}
	names := make([]string, 0, len(sections))
	for name := range sections {
		names = append(names, string(name))
	}
	sort.Strings(names)
	for _, name := range names {
		if utf8.RuneCountInString(sections[SectionName(name)]) > MaxSectionLen {
			return SectionName(name), true
		}
	}
	return "", false
}

// ToMap 与 Python Profile.to_dict 逐字段一致。
func (p Profile) ToMap() map[string]any {
	var last any
	if p.LastUpdatedAt != nil {
		last = *p.LastUpdatedAt
	}
	return map[string]any{
		"id":              string(p.ID),
		"sections":        sectionsToPlain(p.Sections),
		"last_updated_at": last,
	}
}

// ProfileFromMap 从 JSON 解码结构（map[string]any）构造 Profile。
// 用于加载 golden fixtures 与 adapter 层输入。
func ProfileFromMap(d map[string]any) (Profile, error) {
	idAny, ok := d["id"]
	if !ok {
		return Profile{}, &ContractError{Field: "id", Reason: "missing required field"}
	}
	id, ok := idAny.(string)
	if !ok || id == "" {
		return Profile{}, &ContractError{Field: "id", Reason: "must be a non-empty string"}
	}
	// ID 结构校验（RT-2026-08 #34）：ID 原样渲染进 scoring 块头与 intro
	// Person 行，控制字符/逗号/括号可注入受信指令槽位或伪造批量块头。
	for _, r := range id {
		if r < 0x20 || r == ',' || r == '(' || r == ')' {
			return Profile{}, &ContractError{
				Field: "id",
				Reason: "must not contain control characters, comma, or parentheses " +
					"(prompt structure injection surface)",
			}
		}
	}
	sections := map[SectionName]string{}
	if raw, ok := d["sections"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				sections[SectionName(k)] = s
			}
		}
	}
	// 分节长度上限（RT3 #50）：超大分节逐字进入该成员参与的每个
	// LLM/embedding 调用（财务 DoS），注册咽喉 fail-loud 拒绝。
	if name, over := OverlongSection(sections); over {
		return Profile{}, &ContractError{
			Field:  "sections",
			Reason: fmt.Sprintf("section %q exceeds MaxSectionLen=%d runes (financial DoS surface)", name, MaxSectionLen),
		}
	}
	var last *string
	if s, ok := d["last_updated_at"].(string); ok {
		last = &s
	}
	return NewProfile(UserID(id), sections, last), nil
}

// ExtractedSections 是 LLM 提取后的结构化分节（extract 阶段输出）。
//
// Hash 是内容 hash：sha256(python-json-dump(sections, sort_keys))[:16]，
// 驱动 embedding 的 content-addressed 增量复用
// （spec/05-boundaries.md §6：复用以内容为键，不以花名册为键）。
type ExtractedSections struct {
	ID       UserID
	Sections map[SectionName]string
	Hash     string
}

// NewExtractedSections 构造 ExtractedSections；hash 为空时自动计算
// （对应 Python __post_init__）。sections 的 JSON 序列化格式必须与
// Python json.dumps 逐字节一致，否则 hash 不一致、缓存全失效。
func NewExtractedSections(id UserID, sections map[SectionName]string, hash string) ExtractedSections {
	if sections == nil {
		sections = map[SectionName]string{}
	}
	if hash == "" {
		hash = HashText(pyJSONDumpSections(sectionsToPlainStrings(sections)))
	}
	return ExtractedSections{ID: id, Sections: sections, Hash: hash}
}

// SectionHash 返回单个分节的内容 hash（"{user_id}|{section}" 为键的
// bundle 级缓存用它；见 embed 阶段的 section_hashes）。
func (es ExtractedSections) SectionHash(name SectionName) string {
	return HashText(es.Sections[name])
}

// ToMap 与 Python ExtractedSections.to_dict 逐字段一致。
func (es ExtractedSections) ToMap() map[string]any {
	return map[string]any{
		"id":       string(es.ID),
		"sections": sectionsToPlain(es.Sections),
		"hash":     es.Hash,
	}
}

// HydeDescriptors 是 Hypothetical Document Embeddings：每个分节的
// 假设性描述列表（hyde 阶段输出，embed 阶段消费）。
//
// descriptors 为 {section: [desc1, ...]}；n_descriptors 默认 1，可配。
// 支持多描述端到端（描述对之间 max-pool，见 similarity 阶段）。
type HydeDescriptors struct {
	ID          UserID
	Descriptors map[SectionName][]string
}

// ToMap 与 Python HydeDescriptors.to_dict 逐字段一致。
func (h HydeDescriptors) ToMap() map[string]any {
	descs := map[string]any{}
	for k, v := range h.Descriptors {
		list := make([]any, len(v))
		for i, s := range v {
			list[i] = s
		}
		descs[string(k)] = list
	}
	return map[string]any{"id": string(h.ID), "descriptors": descs}
}

// sectionsToPlain 把 map[SectionName]string 转为 JSON 友好的
// map[string]any（编码层统一用 any，避免类型混杂）。
func sectionsToPlain(m map[SectionName]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

// sectionsToPlainStrings 转为 map[string]string（pyJSONDumpSections 的入参）。
func sectionsToPlainStrings(m map[SectionName]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

// ContractError 表示数据违反 spec/01-schemas.md 的 IO 契约。
// 边界处（文件/LLM 输入解析）显式返回，不 panic、不静默吞掉。
type ContractError struct {
	Field  string
	Reason string
}

func (e *ContractError) Error() string {
	return "contract violation: field " + e.Field + ": " + e.Reason
}
