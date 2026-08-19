package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// CreateSectionsDict 把 ExtractedSections 列表转为按 user_id 索引的
// sections 查询表（score / introduce 阶段共用，避免线性扫描）。
func CreateSectionsDict(extracted []domain.ExtractedSections) map[domain.UserID]map[string]string {
	out := make(map[domain.UserID]map[string]string, len(extracted))
	for _, es := range extracted {
		sections := make(map[string]string, len(es.Sections))
		for name, content := range es.Sections {
			sections[string(name)] = content
		}
		out[es.ID] = sections
	}
	return out
}

// GenerateIntroductions 为每对匹配生成双向对接话术 + 破冰话题
// （introduce 阶段，纯变换；LLM 注入）。
//
// LLM 失败 / 响应不可解析 → 模板兜底话术（attachFallbackIntro），
// 保证每条边都有非空 intro / starter_topics，不缺项、不中断。
func GenerateIntroductions(
	edges []domain.Edge,
	sectionsDict map[domain.UserID]map[string]string,
	instruction string,
	promptTemplate string,
	llm LLMClient,
	model string,
) map[domain.PairID]domain.Introduction {
	out := make(map[domain.PairID]domain.Introduction, len(edges))
	for _, edge := range edges {
		prompt := buildIntroPrompt(edge, sectionsDict, instruction, promptTemplate)
		raw, err := llm.Complete(prompt, model)
		var parsed *introPayload
		if err == nil {
			parsed = parseIntroResponse(raw)
		}
		if parsed == nil {
			fallback := AttachFallbackIntro(edge, nil)
			out[edge.PairID] = domain.Introduction{
				PairID:        edge.PairID,
				Intro:         fallback.Intro,
				StarterTopics: fallback.StarterTopics,
			}
			continue
		}
		out[edge.PairID] = domain.Introduction{
			PairID:        edge.PairID,
			Intro:         parsed.Intro,
			StarterTopics: parsed.StarterTopics,
		}
	}
	return out
}

// AttachFallbackIntro 生成模板话术，返回填充了 intro / starter_topics
// 的 Edge 副本（纯函数，不修改原 Edge）。
//
// displayNames 缺省时用 user_id 本身。
func AttachFallbackIntro(edge domain.Edge, displayNames map[string]string) domain.Edge {
	name1 := string(edge.User1)
	name2 := string(edge.User2)
	if n, ok := displayNames[string(edge.User1)]; ok {
		name1 = n
	}
	if n, ok := displayNames[string(edge.User2)]; ok {
		name2 = n
	}
	intro := fmt.Sprintf(
		"For %s: %s looks like a promising connection based on your "+
			"profiles — their background may complement what you're looking for.\n"+
			"For %s: %s looks like a promising connection based on your "+
			"profiles — their background may complement what you're looking for.",
		name1, name2, name2, name1,
	)
	starterTopics := "What each of you is currently working on; where your goals overlap; " +
		"one concrete way you could help each other this month."
	edge.Intro = intro
	edge.StarterTopics = starterTopics
	return edge
}

// introPayload 是话术响应的解析结果。
type introPayload struct {
	Intro         string
	StarterTopics string
}

// buildIntroPrompt 构造双向话术 prompt。
// 占位符缺失渲染为空串（pyFormatMap 的 _safe_format 语义）。
func buildIntroPrompt(
	edge domain.Edge,
	sectionsDict map[domain.UserID]map[string]string,
	instruction string,
	promptTemplate string,
) string {
	return pyFormatMap(promptTemplate, map[string]string{
		"user1_name":     string(edge.User1),
		"user2_name":     string(edge.User2),
		"user1_sections": FormatSections(sectionsDict[edge.User1]),
		"user2_sections": FormatSections(sectionsDict[edge.User2]),
		"instruction":    instruction,
		"user1":          string(edge.User1),
		"user2":          string(edge.User2),
	})
}

// parseIntroResponse 解析 {"intro": str, "starter_topics": str}；
// 失败返回 nil（走 fallback）。容忍 markdown 代码围栏与前后噪声。
func parseIntroResponse(text string) *introPayload {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i != -1 {
			s = s[i+1:]
		}
		s = strings.TrimRight(s, " \t\r\n")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimRight(s, " \t\r\n")
	}
	obj := loadsIntroJSON(s)
	if obj == nil {
		return nil
	}
	intro, _ := obj["intro"].(string)
	topics, _ := obj["starter_topics"].(string)
	if strings.TrimSpace(intro) == "" || strings.TrimSpace(topics) == "" {
		return nil
	}
	return &introPayload{Intro: intro, StarterTopics: topics}
}

// loadsIntroJSON 宽松解析：先整串，再截取首 { 到末 }。
func loadsIntroJSON(s string) map[string]any {
	if s == "" {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err == nil {
		return obj
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start == -1 || end <= start {
		return nil
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &obj); err == nil {
		return obj
	}
	return nil
}
