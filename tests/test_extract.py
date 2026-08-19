"""extract stage 单元测试（离线）。

覆盖 spec/02-stages.md §1 与 spec/05-boundaries.md §4：
- 失败 section 填 "Not specified"，pipeline 继续；
- failed_out 报告失败项（adapter 据此不持久化）；
- 与输入等长、按 id 对齐；hash 用 hash_text(json.dumps(sort_keys))。
"""

import json

from mutual.extract import NOT_SPECIFIED, dump_sections, extract_sections, load_sections
from mutual.schemas import ExtractedSections, Profile, hash_text


class ScriptedLLM:
    """按脚本逐条返回响应的 fake LLM（鸭子类型）。"""

    def __init__(self, responses):
        self.responses = list(responses)
        self.prompts = []

    def __call__(self, messages, **kwargs):
        self.prompts.append(messages[0]["content"])
        return self.responses.pop(0)


_RAISING_MSG = "boom"


class RaisingLLM:
    def __call__(self, messages, **kwargs):
        raise RuntimeError(_RAISING_MSG)


def _profile(uid, sections):
    return Profile(id=uid, sections=sections)


def _good_response():
    return json.dumps(
        {
            "skills": "Python & data viz",
            "vision": "Open-source social impact",
            "project": "Mutual engine",
            "needs": "Frontend collaborator",
        }
    )


class TestExtractSuccess:
    def test_full_success(self, config):
        llm = ScriptedLLM([_good_response(), _good_response()])
        profiles = [
            _profile("alice", {"skills": "raw a"}),
            _profile("bob", {"needs": "raw b"}),
        ]
        failed = []
        out = extract_sections(profiles, config, llm, failed_out=failed)

        assert len(out) == 2
        assert [e.id for e in out] == ["alice", "bob"]
        assert failed == []
        for es in out:
            assert es.sections["skills"] == "Python & data viz"
            assert es.sections["needs"] == "Frontend collaborator"

    def test_hash_is_hash_text_of_sorted_sections(self, config):
        llm = ScriptedLLM([_good_response()])
        (es,) = extract_sections([_profile("alice", {"skills": "x"})], config, llm)
        assert isinstance(es, ExtractedSections)
        assert es.hash == hash_text(json.dumps(es.sections, sort_keys=True))

    def test_prompt_contains_raw_text(self, config):
        llm = ScriptedLLM([_good_response()])
        extract_sections([_profile("alice", {"skills": "Kubernetes wizard"})], config, llm)
        assert "Kubernetes wizard" in llm.prompts[0]

    def test_llm_wrapper_receives_model_from_config(self, config):
        received = {}

        class ModelCaptureLLM:
            def __call__(self, messages, **kwargs):
                received.update(kwargs)
                return _good_response()

        extract_sections([_profile("alice", {})], config, ModelCaptureLLM())
        assert received.get("model") == config["models"]["pair_llm"]


class TestExtractFailure:
    def test_missing_sections_filled_not_specified_and_reported(self, config):
        # 响应只含 skills，其余三节缺失 → 填 "Not specified"，profile 进 failed_out
        llm = ScriptedLLM([json.dumps({"skills": "Python"})])
        failed = []
        (es,) = extract_sections([_profile("alice", {"skills": "raw"})], config, llm, failed)

        assert es.sections["skills"] == "Python"
        for name in ("vision", "project", "needs"):
            assert es.sections[name] == NOT_SPECIFIED
        assert failed == ["alice"]

    def test_not_specified_echo_treated_as_missing(self, config):
        llm = ScriptedLLM([json.dumps({"skills": "Not specified", "vision": "", "needs": "   "})])
        failed = []
        (es,) = extract_sections([_profile("alice", {})], config, llm, failed)
        assert all(v == NOT_SPECIFIED for v in es.sections.values())
        assert failed == ["alice"]

    def test_invalid_json_response(self, config):
        llm = ScriptedLLM(["definitely not json"])
        failed = []
        (es,) = extract_sections([_profile("alice", {})], config, llm, failed)
        assert all(v == NOT_SPECIFIED for v in es.sections.values())
        assert failed == ["alice"]

    def test_code_fenced_json_is_parsed(self, config):
        llm = ScriptedLLM([f"```json\n{_good_response()}\n```"])
        failed = []
        (es,) = extract_sections([_profile("alice", {})], config, llm, failed)
        assert failed == []
        assert es.sections["vision"] == "Open-source social impact"

    def test_llm_exception_is_failure_not_crash(self, config):
        failed = []
        (es,) = extract_sections([_profile("alice", {})], config, RaisingLLM(), failed)
        assert all(v == NOT_SPECIFIED for v in es.sections.values())
        assert failed == ["alice"]

    def test_conftest_fake_llm_route_yields_all_failed(self, config, fake_llm):
        """conftest fake_llm 对非打分 prompt 返回 intro 模板（无 section 键）→ 全部失败。"""
        failed = []
        out = extract_sections(
            [_profile("alice", {"skills": "a"}), _profile("bob", {"needs": "b"})],
            config,
            fake_llm,
            failed,
        )
        assert len(out) == 2
        assert all(v == NOT_SPECIFIED for es in out for v in es.sections.values())
        assert failed == ["alice", "bob"]

    def test_failed_out_optional(self, config):
        llm = ScriptedLLM(["nope"])
        out = extract_sections([_profile("alice", {})], config, llm)  # 不传 failed_out
        assert all(v == NOT_SPECIFIED for v in out[0].sections.values())


class TestDumpLoad:
    def test_roundtrip(self, tmp_path):
        es = ExtractedSections(id="alice", sections={"skills": "Python"})
        path = str(tmp_path / "sections.json")
        dump_sections([es], path)
        loaded = load_sections(path)
        assert loaded == [es]
