// Package pipeline 是管线的编排层（对应 Python src/mutual/runners.py）。
//
// 三种运行模式（docs/engineering-plan.md §3.8）：
//   - RunFullMatch：N×N 全量匹配（cohort 内部互推）；
//   - RunQueryMatch：1×M 查询匹配（单查询对候选池）；
//   - RunBatchMatch：M×N 子集批量匹配（互惠推荐主模式）。
//
// 分层契约（CLAUDE.md §2.3 铁律）：engine 的各阶段是纯变换，本包是
// adapter——唯一允许做 IO 的地方（经 Store 接口）。串联顺序：
//
//	extract → hyde → embed → similarity → select → score
//	       → pre_matrix → match → introduce → report
//
// LLM / embedder 以接口注入（consumer-defined interface）；缺注入时
// fail loud（与 Python _require_llm_wrapper 的 ValueError 一致）。
package pipeline

import (
	"fmt"

	"github.com/Cloudbird-Software/mutual/config"
	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
	"github.com/Cloudbird-Software/mutual/internal/store"
)

// Deps 是管线运行的外部依赖（全部注入，无全局状态）。
type Deps struct {
	// LLM 是 LLM 客户端（extract/hyde/score/introduce 必需）。
	LLM engine.LLMClient
	// Embedder 是 embedding 客户端（embed 阶段必需）。
	Embedder engine.Embedder
	// Store 是可选持久化；nil = 全内存运行，不落盘。
	Store store.Store
}

// validate 校验 LLM 依赖（score/introduce 必需；embedder 只在跑
// embed 阶段时校验——bundle 直入模式不需要）。
func (d Deps) validate() error {
	if d.LLM == nil {
		return fmt.Errorf("LLM 未注入：extract/hyde/score/introduce 需要 LLMClient 实现")
	}
	return nil
}

// validateEmbedder 校验 embedder（跑 extract→hyde→embed 链路时必需）。
func (d Deps) validateEmbedder() error {
	if d.Embedder == nil {
		return fmt.Errorf("Embedder 未注入：embed 阶段需要 Embedder 实现")
	}
	return nil
}

// FullMatchInput 是 RunFullMatch 的输入。
type FullMatchInput struct {
	// Profiles 是原始画像（走完整 extract→hyde→embed 链路）。
	// 与 Bundle 二选一：Profiles 非空时走全链路。
	Profiles []domain.Profile
	// Bundle 是已有 embedding（跳过 extract/hyde/embed，从 similarity
	// 起跑）；Profiles 为空时生效。
	Bundle *domain.EmbeddingsBundle
	// Sections 是 bundle 直入模式下的 sections
	// （打分/话术 prompt 需要；无 store 时的注入通道）。
	Sections []domain.ExtractedSections
	// Existing 是增量复用的旧 bundle（无 store 时的 existing 通道；
	// 有 store 时 store 优先）。
	Existing *domain.EmbeddingsBundle
	// ExcludedPairs 显式 novelty 排除集（优先于 store 历史）。
	ExcludedPairs map[domain.PairID]bool
	// ReferenceEmbed / ReferenceLLM 是归一化参考分布（可选）。
	ReferenceEmbed []float64
	ReferenceLLM   []float64
	// TopMatchesPerUser 每用户报告条数（0 = 不截断；不设时读
	// config reporting.top_matches_per_user）。
	TopMatchesPerUser int
}

// RunFullMatch N×N 全量匹配：全 cohort 内部互相推荐。
//
// min_profiles_required 守卫（spec/05-boundaries.md §7）：画像数不足
// 直接拒绝运行，不产出半成品结果。
func RunFullMatch(in FullMatchInput, cfg *config.Config, deps Deps) (*domain.MatchResult, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}

	excluded := resolveExcludedPairs(in.ExcludedPairs, deps.Store)

	var bundle *domain.EmbeddingsBundle
	var extracted []domain.ExtractedSections

	if len(in.Profiles) > 0 {
		if err := deps.validateEmbedder(); err != nil {
			return nil, err
		}
		minRequired := cfg.MatchingMinProfiles()
		if len(in.Profiles) < minRequired {
			return nil, fmt.Errorf(
				"profile 数 %d 低于 matching.min_profiles_required=%d，拒绝运行",
				len(in.Profiles), minRequired)
		}
		var err error
		bundle, extracted, err = runExtractHydeEmbed(in.Profiles, in.Existing, cfg, deps)
		if err != nil {
			return nil, err
		}
	} else {
		bundle = in.Bundle
		if bundle == nil {
			return nil, fmt.Errorf("FullMatchInput 需要 Profiles 或 Bundle 之一")
		}
		extracted = bundleSections(bundle.UserIDs, in.Sections, deps.Store)
	}

	result, err := runMatchFlow(matchFlowInput{
		sourceBundle:      bundle,
		targetBundle:      nil,
		sourceExtracted:   extracted,
		poolExtracted:     nil,
		cfg:               cfg,
		deps:              deps,
		excludedPairs:     excluded,
		scopeUserIDs:      nil,
		referenceEmbed:    in.ReferenceEmbed,
		referenceLLM:      in.ReferenceLLM,
		topMatchesPerUser: in.TopMatchesPerUser,
	})
	if err != nil {
		return nil, err
	}
	if deps.Store != nil {
		if err := deps.Store.PutMatches(result.Edges); err != nil {
			return nil, fmt.Errorf("持久化匹配结果: %w", err)
		}
	}
	return result, nil
}

// QueryMatchInput 是 RunQueryMatch 的输入。
type QueryMatchInput struct {
	// QueryText 是查询自由文本（广播到全部 section 名，与 pool 对齐）。
	QueryText string
	// QueryID 是查询侧用户 ID（默认 "query"）。
	QueryID string
	// PoolBundle 是候选池的 embedding bundle。
	PoolBundle *domain.EmbeddingsBundle
	// PoolSections 是 pool 侧 sections（打分/话术 prompt 需要）。
	PoolSections []domain.ExtractedSections
	// ExcludedPairs 显式 novelty 排除集（query 模式无 store 通道）。
	ExcludedPairs map[domain.PairID]bool
	// ReferenceEmbed / ReferenceLLM 归一化参考分布（可选）。
	ReferenceEmbed []float64
	ReferenceLLM   []float64
	// TopMatchesPerUser 每用户报告条数（0 = 不截断）。
	TopMatchesPerUser int
}

// RunQueryMatch 1×M 查询匹配：把 query_text 当作单用户与 pool 匹配。
func RunQueryMatch(in QueryMatchInput, cfg *config.Config, deps Deps) (*domain.MatchResult, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	if in.PoolBundle == nil {
		return nil, fmt.Errorf("QueryMatchInput.PoolBundle 未注入")
	}
	if err := deps.validateEmbedder(); err != nil {
		return nil, err
	}
	queryID := in.QueryID
	if queryID == "" {
		queryID = "query"
	}

	// query 文本广播到全部 section 名，保证 query bundle 与 pool 的
	// section_names 对齐（spec 沉默 S5）。
	sections := make(map[domain.SectionName]string, len(in.PoolBundle.SectionNames))
	for _, name := range in.PoolBundle.SectionNames {
		sections[name] = in.QueryText
	}
	queryProfile := domain.NewProfile(domain.UserID(queryID), sections, nil)

	templates, err := cfg.ResolvePromptTemplates(nil)
	if err != nil {
		return nil, err
	}
	models := cfg.Models()
	extracted, _ := engine.ExtractSections(
		[]domain.Profile{queryProfile},
		templates[config.TemplateSection],
		models.PairLLM,
		deps.LLM,
	)
	hyde := engine.GenerateHyde(extracted, cfg.HydeNDescriptors(),
		templates[config.TemplateHyde], models.PairLLM, deps.LLM)
	queryBundle, err := engine.EmbedSections(extracted, hyde, models.Embedding, nil, deps.Embedder)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	return runMatchFlow(matchFlowInput{
		sourceBundle:      queryBundle,
		targetBundle:      in.PoolBundle,
		sourceExtracted:   extracted,
		poolExtracted:     in.PoolSections,
		cfg:               cfg,
		deps:              deps,
		excludedPairs:     in.ExcludedPairs,
		scopeUserIDs:      []domain.UserID{domain.UserID(queryID)},
		referenceEmbed:    in.ReferenceEmbed,
		referenceLLM:      in.ReferenceLLM,
		topMatchesPerUser: in.TopMatchesPerUser,
	})
}

// BatchMatchResult 是 RunBatchMatch 的输出：MatchResult + batch 模式
// 专属元数据（member/pool 侧 ID、被排除的 pair、运行元信息）。
type BatchMatchResult struct {
	MatchResult *domain.MatchResult
	MemberIDs   []domain.UserID
	PoolIDs     []domain.UserID
	// ExcludedPairIDs 是被 novelty 排除的 pair（有序）。
	ExcludedPairIDs []domain.PairID
	Metadata        map[string]any
}

// BatchMatchInput 是 RunBatchMatch 的输入。
type BatchMatchInput struct {
	// MemberIDs 是主动匹配的 member 侧 ID（PoolBundle.UserIDs 的子集）。
	MemberIDs []domain.UserID
	// PoolBundle 是候选池的 embedding bundle。
	PoolBundle *domain.EmbeddingsBundle
	// PoolSections 是 pool 侧 sections（member 子集与打分 prompt 需要）。
	PoolSections []domain.ExtractedSections
	// ExcludedPairs novelty 排除集（来自 match_history，§8）。
	ExcludedPairs map[domain.PairID]bool
	// ReferenceEmbed / ReferenceLLM 归一化参考分布（可选）。
	ReferenceEmbed []float64
	ReferenceLLM   []float64
	// TopMatchesPerUser 每用户报告条数（0 = 不截断）。
	TopMatchesPerUser int
}

// RunBatchMatch M×N 子集批量匹配（互惠推荐主模式）。
//
// member 侧从 pool_bundle 取子集，与整个 pool 做 M×N 相似度；度约束
// b_min/b_max 绑定 member 侧（spec/05-boundaries.md §7）。报告范围
// 限定在 member 侧。
func RunBatchMatch(in BatchMatchInput, cfg *config.Config, deps Deps) (*BatchMatchResult, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	if in.PoolBundle == nil {
		return nil, fmt.Errorf("BatchMatchInput.PoolBundle 未注入")
	}
	if len(in.MemberIDs) == 0 {
		return nil, fmt.Errorf("BatchMatchInput.MemberIDs 为空")
	}

	// Python 基线 runners.py: member_set = set(member_ids)——填充后
	// 再筛选（CodeRabbit：漏掉填充循环会让 memberExtracted 恒为空，
	// member 侧 sections 的优先级语义失效）。
	memberSet := make(map[domain.UserID]bool, len(in.MemberIDs))
	for _, id := range in.MemberIDs {
		memberSet[id] = true
	}
	var memberExtracted []domain.ExtractedSections
	for _, es := range in.PoolSections {
		if memberSet[es.ID] {
			memberExtracted = append(memberExtracted, es)
		}
	}

	memberBundle, err := in.PoolBundle.Subset(in.MemberIDs)
	if err != nil {
		return nil, fmt.Errorf("member 子集: %w", err)
	}

	result, meta, err := runMatchFlowWithMeta(matchFlowInput{
		sourceBundle:      memberBundle,
		targetBundle:      in.PoolBundle,
		sourceExtracted:   memberExtracted,
		poolExtracted:     in.PoolSections,
		cfg:               cfg,
		deps:              deps,
		excludedPairs:     in.ExcludedPairs,
		scopeUserIDs:      append([]domain.UserID(nil), in.MemberIDs...),
		referenceEmbed:    in.ReferenceEmbed,
		referenceLLM:      in.ReferenceLLM,
		topMatchesPerUser: in.TopMatchesPerUser,
	})
	if err != nil {
		return nil, err
	}

	var excludedIDs []domain.PairID
	for pid, excluded := range in.ExcludedPairs {
		if excluded {
			excludedIDs = append(excludedIDs, pid)
		}
	}
	sortPairIDs(excludedIDs)

	poolIDs := append([]domain.UserID(nil), in.PoolBundle.UserIDs...)
	return &BatchMatchResult{
		MatchResult:     result,
		MemberIDs:       append([]domain.UserID(nil), in.MemberIDs...),
		PoolIDs:         poolIDs,
		ExcludedPairIDs: excludedIDs,
		Metadata:        meta,
	}, nil
}

// ---------------------------------------------------------------------------
// 内部：extract → hyde → embed 前置链路
// ---------------------------------------------------------------------------

// runExtractHydeEmbed 跑前置三阶段并按需持久化。
func runExtractHydeEmbed(
	profiles []domain.Profile,
	existing *domain.EmbeddingsBundle,
	cfg *config.Config,
	deps Deps,
) (*domain.EmbeddingsBundle, []domain.ExtractedSections, error) {
	templates, err := cfg.ResolvePromptTemplates(nil)
	if err != nil {
		return nil, nil, err
	}
	models := cfg.Models()

	extracted, failedIDs := engine.ExtractSections(
		profiles, templates[config.TemplateSection], models.PairLLM, deps.LLM)

	// 失败提取不落盘（spec/05-boundaries.md §4，否则永远不会重试）。
	if deps.Store != nil {
		failed := make(map[domain.UserID]bool, len(failedIDs))
		for _, id := range failedIDs {
			failed[id] = true
		}
		var persistable []domain.ExtractedSections
		for _, es := range extracted {
			if !failed[es.ID] {
				persistable = append(persistable, es)
			}
		}
		if err := deps.Store.PutSections(persistable); err != nil {
			return nil, nil, fmt.Errorf("持久化 sections: %w", err)
		}
	}

	hyde := engine.GenerateHyde(extracted, cfg.HydeNDescriptors(),
		templates[config.TemplateHyde], models.PairLLM, deps.LLM)

	var reuse *domain.EmbeddingsBundle
	if deps.Store != nil {
		b, err := deps.Store.GetEmbeddings()
		if err != nil {
			return nil, nil, fmt.Errorf("读取已有 embedding: %w", err)
		}
		reuse = b
	} else {
		reuse = existing
	}

	bundle, err := engine.EmbedSections(extracted, hyde, models.Embedding, reuse, deps.Embedder)
	if err != nil {
		return nil, nil, fmt.Errorf("embed: %w", err)
	}
	if deps.Store != nil {
		if err := deps.Store.PutEmbeddings(bundle); err != nil {
			return nil, nil, fmt.Errorf("持久化 embedding: %w", err)
		}
	}
	return bundle, extracted, nil
}

// bundleSections bundle 直入模式：sections 从 store 或入参取。
func bundleSections(userIDs []domain.UserID, sections []domain.ExtractedSections, st store.Store) []domain.ExtractedSections {
	if st != nil {
		byID, err := st.GetSections(userIDs)
		if err == nil {
			var out []domain.ExtractedSections
			for _, uid := range userIDs {
				if es, ok := byID[uid]; ok {
					out = append(out, es)
				}
			}
			return out
		}
	}
	wanted := make(map[domain.UserID]bool, len(userIDs))
	for _, uid := range userIDs {
		wanted[uid] = true
	}
	var out []domain.ExtractedSections
	for _, es := range sections {
		if wanted[es.ID] {
			out = append(out, es)
		}
	}
	return out
}

// resolveExcludedPairs novelty 排除集：显式传入优先，否则从 store 的
// match_history 构建（spec/05-boundaries.md §8）。
func resolveExcludedPairs(explicit map[domain.PairID]bool, st store.Store) map[domain.PairID]bool {
	if explicit != nil {
		return explicit
	}
	if st == nil {
		return nil
	}
	history, err := st.GetMatchHistory()
	if err != nil {
		return nil
	}
	out := make(map[domain.PairID]bool, len(history))
	for _, rec := range history {
		out[rec.PairID] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// 内部：similarity → select → score → pre_matrix → match → introduce → report
// ---------------------------------------------------------------------------

type matchFlowInput struct {
	sourceBundle      *domain.EmbeddingsBundle
	targetBundle      *domain.EmbeddingsBundle
	sourceExtracted   []domain.ExtractedSections
	poolExtracted     []domain.ExtractedSections
	cfg               *config.Config
	deps              Deps
	excludedPairs     map[domain.PairID]bool
	scopeUserIDs      []domain.UserID
	referenceEmbed    []float64
	referenceLLM      []float64
	topMatchesPerUser int
}

// runMatchFlow 跑匹配主链路（不带 meta 返回）。
func runMatchFlow(in matchFlowInput) (*domain.MatchResult, error) {
	result, _, err := runMatchFlowWithMeta(in)
	return result, err
}

// runMatchFlowWithMeta 跑匹配主链路并返回运行元信息（batch 模式消费）。
func runMatchFlowWithMeta(in matchFlowInput) (*domain.MatchResult, map[string]any, error) {
	cfg := in.cfg
	models := cfg.Models()
	templates, err := cfg.ResolvePromptTemplates(nil)
	if err != nil {
		return nil, nil, err
	}
	recipe := cfg.Recipe()

	similarity := engine.ComputeSimilarity(in.sourceBundle, in.targetBundle, cfg.RecipeConfig())
	selected := engine.SelectPairs(similarity, selectBudgets(cfg), in.excludedPairs)

	// sections 查询表：source + pool，后者覆盖前者（Python dict 语义）。
	sectionsDict := engine.CreateSectionsDict(concatExtracted(in.sourceExtracted, in.poolExtracted))

	pairScores, unscored := engine.ScorePairs(
		selected,
		sectionsDict,
		recipe.Instruction,
		templates[config.TemplateScoring],
		in.deps.LLM,
		engine.ScoreBudgets{
			PerProfileCap: intPtrIfPositive(cfg.Budgets().PerProfileCap),
			MaxCalls:      intPtrIfPositive(cfg.Budgets().MaxCalls),
			BatchSize:     cfg.Budgets().BatchSize,
			Model:         models.PairLLM,
		},
	)
	pairScores = engine.PrepareNormalizedScores(pairScores, in.referenceEmbed, in.referenceLLM)

	var prefMatrix *domain.PrefMatrix
	if in.targetBundle != nil {
		// 二部图模式（query/batch）：member×pool 矩形矩阵。
		prefMatrix = engine.BuildBipartitePrefMatrix(
			pairScores, in.sourceBundle.UserIDs, in.targetBundle.UserIDs)
	} else {
		// 同集模式（full）：N×N 方阵。
		prefMatrix = engine.BuildPrefMatrix(pairScores, in.sourceBundle.UserIDs)
	}

	outcome := engine.SolveMatch(prefMatrix, cfg.Matching(), cfg.Blending())

	introductions := engine.GenerateIntroductions(
		outcome.Edges, sectionsDict, recipe.Instruction,
		templates[config.TemplateIntroduction], in.deps.LLM, models.PairLLM)
	edges := make([]domain.Edge, len(outcome.Edges))
	for i, edge := range outcome.Edges {
		if intro, ok := introductions[edge.PairID]; ok {
			edge.Intro = intro.Intro
			edge.StarterTopics = intro.StarterTopics
		} else {
			edge = engine.AttachFallbackIntro(edge, nil)
		}
		edges[i] = edge
	}

	extractedAll := dedupeExtracted(concatExtracted(in.sourceExtracted, in.poolExtracted))
	top := in.topMatchesPerUser
	if top == 0 {
		top = cfg.TopMatchesPerUser()
	}
	reportData := engine.CreateReport(edges, extractedAll, top, in.scopeUserIDs)
	if len(unscored) > 0 {
		notes, _ := reportData["notes"].([]string)
		notes = append(notes, fmt.Sprintf(
			"%d 个候选对因预算/解析失败未获 LLM 打分，保留 embedding 权重（spec/05-boundaries.md §3）。",
			len(unscored)))
		reportData["notes"] = notes
	}

	newPairs := make([]map[string]any, len(edges))
	for i, e := range edges {
		newPairs[i] = map[string]any{
			"pair_id": string(e.PairID),
			"user1":   string(e.User1),
			"user2":   string(e.User2),
		}
	}
	result := &domain.MatchResult{
		Edges:      edges,
		ReportData: reportData,
		NewPairs:   newPairs,
		EnvyReport: outcome.EnvyReport,
	}
	meta := map[string]any{
		"match_fallback":   false,
		"n_selected_pairs": len(selected),
		"n_scored_pairs":   len(selected) - len(unscored),
		"n_unscored_pairs": len(unscored),
	}
	return result, meta, nil
}

// concatExtracted 拼接两段 sections（保序；不去重——
// sections_dict 要"后者覆盖"，报告侧另有 dedupe）。
func concatExtracted(a, b []domain.ExtractedSections) []domain.ExtractedSections {
	out := make([]domain.ExtractedSections, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

// dedupeExtracted 按 id 去重保序（保留首次出现；报告的画像全集口径）。
func dedupeExtracted(extracted []domain.ExtractedSections) []domain.ExtractedSections {
	seen := make(map[domain.UserID]bool, len(extracted))
	out := make([]domain.ExtractedSections, 0, len(extracted))
	for _, es := range extracted {
		if seen[es.ID] {
			continue
		}
		seen[es.ID] = true
		out = append(out, es)
	}
	return out
}

// selectBudgets 把 config 的 budgets 段映射为 select 阶段预算
// （0/null = 不设上限 → nil 指针）。
func selectBudgets(cfg *config.Config) engine.SelectBudgets {
	b := cfg.Budgets()
	return engine.SelectBudgets{
		PerProfileCap: intPtrIfPositive(b.PerProfileCap),
		GlobalCap:     intPtrIfPositive(b.MaxCalls),
	}
}

// intPtrIfPositive n > 0 时返回 &n，否则 nil（与 Python None 语义对齐）。
func intPtrIfPositive(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

// sortPairIDs 字典序排序（ExcludedPairIDs 的确定性输出）。
func sortPairIDs(ids []domain.PairID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}
