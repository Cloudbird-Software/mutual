package holdout

// MT11–MT15：隐藏的 metamorphic 变体（实现者只知道 issue #7 的 MT1–MT10）。
//
// 设计口径（作者自定，冻结）：
//   - 阈值与 issue #7 §4.2 同量级：资格翻转必须发生；偏好级扰动 level 变化 ≤1 级
//     或 u_hat 变化 ≤0.10；无关噪声 u_hat 变化 ≤0.05。
//   - 基线 profile 内嵌于本文件，holdout 套件自包含，不依赖 synth 生成的世界。

import (
	"math"
	"strings"
	"testing"
)

const focalZH = "我是新锐功能食品品牌「山萃」的联合创始人，负责供应链与渠道。品牌已完成 A 轮，" +
	"天猫月销 300 万。现在最紧要的是线下冷启动：需要能直接落地 KA 卖场进场与陈列执行的团队，" +
	"华东优先。硬约束：合作方必须在中国大陆有实体团队，服务费预算上限 50 万/年。" +
	"另外我们希望对方本月就能进场谈判，时间很紧。"

// MT13 用：focal 的需求段换成英文表达（其余不变）
const focalENSeeks = "我是新锐功能食品品牌「山萃」的联合创始人，负责供应链与渠道。品牌已完成 A 轮，" +
	"天猫月销 3M RMB。What I need most right now is a team that can land us into " +
	"KA retail stores in East China and execute in-store displays. " +
	"硬约束：合作方必须在中国大陆有实体团队，服务费预算上限 50 万/年。" +
	"另外我们希望对方本月就能进场谈判，时间很紧。"

const matchOK = "我们是一家专注消费品进场的执行公司，base 上海，实体团队 40 人。" +
	"服务：KA 卖场进场谈判、条码费议价、端架/堆头陈列执行，华东覆盖 300 家门店。" +
	"收费模式：年度服务费 35–45 万，按门店数阶梯。本月可启动新品牌进场。" +
	"案例：2024 年帮两个食品饮料品牌完成华东 KA 冷启动，3 个月进场 120 家。"

// MT11 用：预算违规变体（仅服务费一句不同）
var matchOverBudget = strings.Replace(matchOK, "年度服务费 35–45 万", "年度服务费 80 万起", 1)

// MT12 用：时间冲突变体（仅末尾一句不同）
var matchTimingLate = strings.Replace(matchOK, "本月可启动新品牌进场。", "明年 Q2 之前我们不接任何新品牌合作。", 1)

// MT14 用：导入噪声（重复段落 + 表单残留 + CSV 头），信息零增量
var matchNoisy = "【自我介绍】\n字段：公司：\nname,company,role,updated_at\n" +
	matchOK + "\n" + matchOK + "\n（以上信息由批量导入工具生成）"

const distractor1 = "我是一支早期硬科技基金的投资经理，主要看半导体与机器人方向，单笔 500–2000 万。" +
	"只聊融资，不做渠道。"
const distractor2 = "独立品牌设计师，做包装与 VI，服务过茶饮与美妆客户，周期 4–6 周，不接驻场项目。"

func mtWorld(focal, matchVariant string) map[string]string {
	return map[string]string{
		"focal": focal,
		"match": matchVariant,
		"d1":    distractor1,
		"d2":    distractor2,
	}
}

// MT11 预算硬约束翻转
func TestMT11BudgetHardConstraintFlip(t *testing.T) {
	requireUnlock(t)
	ok := runWorld(t, mtWorld(focalZH, matchOK))
	bad := runWorld(t, mtWorld(focalZH, matchOverBudget))
	if !ok.IsEligible("focal", "match") {
		t.Fatal("基线对必须 eligible")
	}
	if bad.IsEligible("focal", "match") {
		t.Fatal("对方服务费 80 万起 > focal 预算上限 50 万：eligible 必须翻转为 false")
	}
	reason := strings.ToLower(bad.ReasonOf("focal", "match"))
	hit := false
	for _, k := range []string{"budget", "预算", "cost", "fee", "hard"} {
		if strings.Contains(reason, k) {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("reason_code 必须指向预算类硬约束，实际: %q", reason)
	}
}

// MT12 时间窗口冲突（偏好级，不是资格级）
func TestMT12TimingWindowConflict(t *testing.T) {
	requireUnlock(t)
	ok := runWorld(t, mtWorld(focalZH, matchOK))
	late := runWorld(t, mtWorld(focalZH, matchTimingLate))
	if !late.IsEligible("focal", "match") {
		t.Fatal("时间冲突不构成资格否决")
	}
	if lv := late.LevelOf("focal", "match"); lv > 1 {
		t.Fatalf("focal 需求 urgency=now，对方明年 Q2 才接新合作：level 必须 ≤1，实得 %d", lv)
	}
	if lv := ok.LevelOf("focal", "match"); lv < 2 {
		t.Fatalf("基线（本月可启动）应 ≥2，实得 %d", lv)
	}
}

// MT13 跨语言表达不变性
func TestMT13CrossLingualInvariance(t *testing.T) {
	requireUnlock(t)
	zh := runWorld(t, mtWorld(focalZH, matchOK))
	en := runWorld(t, mtWorld(focalENSeeks, matchOK))
	delta := math.Abs(zh.UHat("focal", "match") - en.UHat("focal", "match"))
	if delta > 0.10 {
		t.Fatalf("同一需求用中英文表达，u_hat 漂移 %.3f > 0.10（跨语言偏置）", delta)
	}
}

// MT14 批量导入噪声鲁棒性
func TestMT14ImportNoiseInvariance(t *testing.T) {
	requireUnlock(t)
	clean := runWorld(t, mtWorld(focalZH, matchOK))
	noisy := runWorld(t, mtWorld(focalZH, matchNoisy))
	delta := math.Abs(clean.UHat("focal", "match") - noisy.UHat("focal", "match"))
	if delta > 0.05 {
		t.Fatalf("零信息增量的导入噪声导致 u_hat 漂移 %.3f > 0.05（噪声敏感）", delta)
	}
}

// MT15 身份交换对称性（求解与打分的 id 无关性）
func TestMT15IdentityRelabelingInvariance(t *testing.T) {
	requireUnlock(t)
	world := mtWorld(focalZH, matchOK)
	base := runWorld(t, world)
	relabeled := map[string]string{
		"focal": world["focal"],
		"match": world["match"],
		"x1":    world["d1"],
		"x2":    world["d2"],
	}
	alt := runWorld(t, relabeled)

	mapping := map[string]string{"focal": "focal", "match": "match", "d1": "x1", "d2": "x2"}
	norm := func(matching [][2]string) map[[2]string]bool {
		out := map[[2]string]bool{}
		for _, e := range matching {
			a, b := mapping[e[0]], mapping[e[1]]
			if a > b {
				a, b = b, a
			}
			out[[2]string{a, b}] = true
		}
		return out
	}
	mb, ma := norm(base.Matching), norm(alt.Matching)
	if len(mb) != len(ma) {
		t.Fatalf("重标注后匹配数不同: %d vs %d", len(mb), len(ma))
	}
	for e := range mb {
		if !ma[e] {
			t.Fatalf("重标注后匹配 %v 丢失（身份无关性失败）", e)
		}
	}
	for _, fc := range [][2]string{{"focal", "match"}, {"focal", "d1"}, {"match", "d2"}} {
		f2, c2 := mapping[fc[0]], mapping[fc[1]]
		if base.UHat(fc[0], fc[1]) != alt.UHat(f2, c2) {
			t.Fatalf("重标注后 (%s,%s) 的 u_hat 不一致", fc[0], fc[1])
		}
	}
}
