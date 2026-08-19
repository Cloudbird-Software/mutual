"""introduce 阶段测试：LLM 话术生成 + fallback 兜底。"""

from mutual.config import resolve_prompt_templates
from mutual.introduce import attach_fallback_intro, generate_introductions_for_matches
from mutual.schemas import Edge, ExtractedSections
from mutual.score import create_sections_dict


def _edge(u1: str, u2: str, weight: float = 0.5) -> Edge:
    return Edge(
        user1=u1,
        user2=u2,
        pair_id=f"{u1}__{u2}",
        final_weight=weight,
        embed_score=0.4,
        llm_score=0.6,
    )


def _sections(*uids):
    return [
        ExtractedSections(id=uid, sections={"skills": f"{uid} skills", "needs": f"{uid} needs"})
        for uid in uids
    ]


class TestGenerateIntroductions:
    def test_llm_generated_intro(self, fake_llm, config):
        """非打分类 prompt（不含 a_to_b）→ fake 返回固定 intro 模板。"""
        edges = [_edge("alice", "bob"), _edge("carol", "david")]
        intros = generate_introductions_for_matches(
            edges,
            create_sections_dict(_sections("alice", "bob", "carol", "david")),
            instruction=config["recipe"]["instruction"],
            prompt_template=resolve_prompt_templates(config)["introduction"],
            llm_wrapper=fake_llm,
        )
        assert set(intros) == {"alice__bob", "carol__david"}
        assert intros["alice__bob"].intro == "Fake intro."
        assert intros["alice__bob"].starter_topics == "Fake topic."

    def test_fallback_on_llm_exception(self, config):
        class BoomLLM:
            def __call__(self, messages, **kwargs):
                raise RuntimeError("llm down")

        edges = [_edge("alice", "bob")]
        intros = generate_introductions_for_matches(
            edges,
            create_sections_dict(_sections("alice", "bob")),
            instruction="instr",
            prompt_template=resolve_prompt_templates(config)["introduction"],
            llm_wrapper=BoomLLM(),
        )
        intro = intros["alice__bob"]
        assert intro.intro.strip()
        assert intro.starter_topics.strip()
        assert "alice" in intro.intro and "bob" in intro.intro

    def test_fallback_on_bad_response(self, config):
        class GarbageLLM:
            def __call__(self, messages, **kwargs):
                return "<html>not json</html>"

        edges = [_edge("alice", "bob")]
        intros = generate_introductions_for_matches(
            edges,
            create_sections_dict(_sections("alice", "bob")),
            instruction="instr",
            prompt_template=resolve_prompt_templates(config)["introduction"],
            llm_wrapper=GarbageLLM(),
        )
        assert "For alice" in intros["alice__bob"].intro
        assert "For bob" in intros["alice__bob"].intro

    def test_fallback_on_empty_fields(self, config):
        class EmptyLLM:
            def __call__(self, messages, **kwargs):
                return '{"intro": "  ", "starter_topics": ""}'

        intros = generate_introductions_for_matches(
            [_edge("alice", "bob")],
            create_sections_dict(_sections("alice", "bob")),
            instruction="instr",
            prompt_template=resolve_prompt_templates(config)["introduction"],
            llm_wrapper=EmptyLLM(),
        )
        assert intros["alice__bob"].intro.strip()

    def test_display_names_used_in_fallback(self, config):
        class BoomLLM:
            def __call__(self, messages, **kwargs):
                raise RuntimeError("down")

        intros = generate_introductions_for_matches(
            [_edge("u1", "u2")],
            create_sections_dict(_sections("u1", "u2")),
            instruction="instr",
            prompt_template=resolve_prompt_templates(config)["introduction"],
            llm_wrapper=BoomLLM(),
            display_names={"u1": "Alice Zhang", "u2": "Bob Li"},
        )
        assert "Alice Zhang" in intros["u1__u2"].intro
        assert "Bob Li" in intros["u1__u2"].intro

    def test_no_missing_entries(self, fake_llm, config):
        edges = [_edge(f"u{i}", f"u{i + 1}") for i in range(5)]
        intros = generate_introductions_for_matches(
            edges,
            create_sections_dict([]),
            instruction="instr",
            prompt_template=resolve_prompt_templates(config)["introduction"],
            llm_wrapper=fake_llm,
        )
        assert set(intros) == {e.pair_id for e in edges}
        assert all(intros[e.pair_id].pair_id == e.pair_id for e in edges)

    def test_json_with_code_fence_parsed(self, config):
        class FencedLLM:
            def __call__(self, messages, **kwargs):
                return '```json\n{"intro": "Fenced.", "starter_topics": "Topics."}\n```'

        intros = generate_introductions_for_matches(
            [_edge("alice", "bob")],
            create_sections_dict(_sections("alice", "bob")),
            instruction="instr",
            prompt_template=resolve_prompt_templates(config)["introduction"],
            llm_wrapper=FencedLLM(),
        )
        assert intros["alice__bob"].intro == "Fenced."


class TestAttachFallbackIntro:
    def test_returns_copy_original_untouched(self):
        edge = _edge("alice", "bob")
        out = attach_fallback_intro(edge)
        assert out is not edge
        assert edge.intro == ""
        assert edge.starter_topics == ""
        assert out.intro.strip()
        assert out.starter_topics.strip()

    def test_bidirectional_template(self):
        out = attach_fallback_intro(_edge("alice", "bob"))
        assert "For alice" in out.intro
        assert "For bob" in out.intro

    def test_display_names(self):
        out = attach_fallback_intro(_edge("u1", "u2"), display_names={"u1": "张三", "u2": "李四"})
        assert "张三" in out.intro
        assert "李四" in out.intro

    def test_preserves_edge_fields(self):
        edge = _edge("alice", "bob", weight=0.77)
        out = attach_fallback_intro(edge)
        assert out.pair_id == edge.pair_id
        assert out.final_weight == edge.final_weight
        assert out.llm_score == edge.llm_score
