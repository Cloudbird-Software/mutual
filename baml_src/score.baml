// score 阶段的 LLM 契约（spec/02-stages.md §6）：
// 候选对**双向**打分（A→B 与 B→A 分别打分，方向性不盲目对称化）。
//
// 批量语义：一次调用打一批对（budgets.n_profiles_to_score_together），
// 与 Python 基线 score_pairs_with_llm 的 batch prompt 同构；
// instruction 来自 config recipe.instruction（参数注入，非硬编码）。
class PairScoringInput {
  user1 string @description("First user's id")
  user2 string @description("Second user's id")
  user1_sections string @description("Formatted sections of user1 ('key: value' lines)")
  user2_sections string @description("Formatted sections of user2 ('key: value' lines)")
}

class DirectionalPairScore {
  user1 string @description("Echo of input user1 (positional alignment)")
  user2 string @description("Echo of input user2 (positional alignment)")
  a_to_b float @description("How valuable is this connection for user1? 0.0-1.0")
  b_to_a float @description("How valuable is this connection for user2? 0.0-1.0")
  reasoning string @description("Brief justification")
}

function ScorePairs(
  pairs: PairScoringInput[],
  instruction: string
) -> DirectionalPairScore[] {
  client "openai/gpt-4o-mini"
  prompt #"
    You are a matchmaking expert. Score the potential connection between two people.

    Instruction: {{ instruction }}

    Score each pair below in both directions:
    1. How valuable is this connection for user1? (a_to_b score, 0.0-1.0)
    2. How valuable is this connection for user2? (b_to_a score, 0.0-1.0)

    Respond with exactly one score object per input pair, in the same order,
    echoing each pair's user1/user2 ids.

    Pairs to score:
    {{ pairs|text_block }}
  "#
}
