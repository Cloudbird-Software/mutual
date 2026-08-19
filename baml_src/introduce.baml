// introduce 阶段的 LLM 契约（spec/02-stages.md §9）：
// 为匹配对生成双向对接话术 + 破冰话题。
//
// LLM 失败时引擎回退 attach_fallback_intro 模板话术
// （engine 包，纯函数），保证每条 Edge 都有非空 intro/starter_topics。
class IntroductionDraft {
  intro string @description("Two paragraphs: 'For {user1_name}: ...' and 'For {user2_name}: ...'")
  starter_topics string @description("2-3 starter topics for their first conversation")
}

function DraftIntroduction(
  user1_name: string,
  user1_sections: string,
  user2_name: string,
  user2_sections: string,
  instruction: string
) -> IntroductionDraft {
  client "openai/gpt-4o-mini"
  prompt #"
    Write a personalized introduction for a matched pair.

    Person A: {{ user1_name }}
    {{ user1_sections }}

    Person B: {{ user2_name }}
    {{ user2_sections }}

    Instruction: {{ instruction }}

    Write two paragraphs:
    - "For {{ user1_name }}: ..." explaining why they should connect with Person B.
    - "For {{ user2_name }}: ..." explaining why they should connect with Person A.

    Also suggest 2-3 starter topics for their first conversation.
  "#
}
