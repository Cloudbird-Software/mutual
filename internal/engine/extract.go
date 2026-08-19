package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// ExtractSections 用 LLM 从自由文本画像提取结构化分节
// （extract 阶段，纯变换；LLM 经 LLMClient 注入）。
//
// 输出与输入等长、按 id 对齐；失败分节填 NotSpecified，pipeline 继续。
// failedIDs 报告失败项（任一分节缺失即失败）——adapter 不得持久化
// 失败结果（spec/05-boundaries.md §4，否则永远不会重试）。
//
// Go/Python 差异（已文档化）：Python 的 raw_text 按画像 dict 的插入序
// 拼接；Go 的 map 无序，此处按分节名排序拼接。对 prompt 语义无影响，
// 对 FakeLLM 路由（按 cohort id 子串）无影响。
func ExtractSections(
	profiles []domain.Profile,
	template string,
	model string,
	llm LLMClient,
) (extracted []domain.ExtractedSections, failedIDs []domain.UserID) {
	extracted = make([]domain.ExtractedSections, 0, len(profiles))
	for _, profile := range profiles {
		parsed := extractOne(profile, template, model, llm)

		sections := make(map[domain.SectionName]string, len(CanonicalSections))
		failed := false
		for _, name := range CanonicalSections {
			if value, ok := parsed[name]; ok && isPresent(value) {
				sections[domain.SectionName(name)] = strings.TrimSpace(value)
				continue
			}
			sections[domain.SectionName(name)] = NotSpecified
			failed = true
		}
		if failed {
			failedIDs = append(failedIDs, profile.ID)
		}
		extracted = append(extracted, domain.NewExtractedSections(profile.ID, sections, ""))
	}
	return extracted, failedIDs
}

// extractOne 跑单个画像的 LLM 提取；任何失败 → nil（调用方填占位值）。
func extractOne(profile domain.Profile, template, model string, llm LLMClient) map[string]string {
	rawText := formatProfileRawText(profile)
	prompt := pyFormatMap(template, map[string]string{"raw_text": rawText})
	response, err := llm.CompleteExtract(prompt, model)
	if err != nil {
		return nil
	}
	return parseExtractResponse(response)
}

// formatProfileRawText 把画像分节渲染为 "name: text" 行（按分节名排序）。
func formatProfileRawText(profile domain.Profile) string {
	names := make([]string, 0, len(profile.Sections))
	for name := range profile.Sections {
		names = append(names, string(name))
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("%s: %s", name, profile.Sections[domain.SectionName(name)]))
	}
	return strings.Join(lines, "\n")
}

// parseExtractResponse 解析 LLM 响应为 {section: text}；
// 容错提取 JSON 主体（首个 { 到末个 }），不可解析返回 nil。
func parseExtractResponse(response string) map[string]string {
	start := strings.IndexByte(response, '{')
	end := strings.LastIndexByte(response, '}')
	if start == -1 || end <= start {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(response[start:end+1]), &data); err != nil {
		return nil
	}
	out := make(map[string]string, len(data))
	for k, v := range data {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// isPresent 判定分节值是否为有效内容（非空、非占位）。
func isPresent(value string) bool {
	stripped := strings.TrimSpace(value)
	return stripped != "" && !strings.EqualFold(stripped, NotSpecified)
}
