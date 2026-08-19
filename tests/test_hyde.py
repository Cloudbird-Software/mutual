"""hyde stage 单元测试（离线）。

覆盖 spec/02-stages.md §2：
- 每个 section 生成 n_descriptors 个假设性描述（config hyde.n_descriptors）；
- "Not specified" section 不生成描述符（缺失 = 中性）。
"""

import json

from mutual.extract import NOT_SPECIFIED
from mutual.hyde import dump_hyde, generate_hyde, load_hyde
from mutual.schemas import ExtractedSections


class ScriptedLLM:
    def __init__(self, response):
        self.response = response
        self.prompts = []

    def __call__(self, messages, **kwargs):
        self.prompts.append(messages[0]["content"])
        return self.response


def _sections():
    return [
        ExtractedSections(
            id="alice",
            sections={
                "skills": "Python & data viz",
                "vision": "Open-source impact",
                "project": NOT_SPECIFIED,
                "needs": NOT_SPECIFIED,
            },
        )
    ]


class TestGenerateHyde:
    def test_default_n_descriptors_is_one_per_section(self, config):
        assert config["hyde"]["n_descriptors"] == 1
        llm = ScriptedLLM("A person who builds data tools.")
        hyde = generate_hyde(_sections(), config, llm)

        assert set(hyde.keys()) == {"alice"}
        descriptors = hyde["alice"].descriptors
        # "Not specified" 的 project/needs 不生成描述符
        assert set(descriptors.keys()) == {"skills", "vision"}
        assert descriptors["skills"] == ["A person who builds data tools."]
        assert descriptors["vision"] == ["A person who builds data tools."]

    def test_n_descriptors_from_config(self, config):
        config = dict(config)
        config["hyde"] = {"n_descriptors": 2}
        llm = ScriptedLLM("First hypothetical description.\nSecond hypothetical description.")
        hyde = generate_hyde(_sections(), config, llm)

        assert hyde["alice"].descriptors["skills"] == [
            "First hypothetical description.",
            "Second hypothetical description.",
        ]

    def test_json_list_response(self, config):
        llm = ScriptedLLM(json.dumps(["desc one", "desc two", "desc three"]))
        config = dict(config)
        config["hyde"] = {"n_descriptors": 2}
        hyde = generate_hyde(_sections(), config, llm)
        assert hyde["alice"].descriptors["skills"] == ["desc one", "desc two"]

    def test_bullet_lines_stripped(self, config):
        llm = ScriptedLLM("- alpha\n* beta\n3. gamma")
        config = dict(config)
        config["hyde"] = {"n_descriptors": 3}
        hyde = generate_hyde(_sections(), config, llm)
        assert hyde["alice"].descriptors["skills"] == ["alpha", "beta", "gamma"]

    def test_prompt_contains_section_and_count(self, config):
        llm = ScriptedLLM("d")
        config = dict(config)
        config["hyde"] = {"n_descriptors": 3}
        generate_hyde(_sections(), config, llm)
        assert "skills" in llm.prompts[0]
        assert "Python & data viz" in llm.prompts[0]
        assert "3" in llm.prompts[0]

    def test_llm_failure_yields_no_descriptors_not_crash(self, config):
        class RaisingLLM:
            def __call__(self, messages, **kwargs):
                raise RuntimeError("boom")

        hyde = generate_hyde(_sections(), config, RaisingLLM())
        assert hyde["alice"].descriptors == {}

    def test_result_indexed_by_user_id(self, config):
        sections = [
            ExtractedSections(id="alice", sections={"skills": "a"}),
            ExtractedSections(id="bob", sections={"skills": "b"}),
        ]
        llm = ScriptedLLM("d")
        hyde = generate_hyde(sections, config, llm)
        assert set(hyde.keys()) == {"alice", "bob"}
        assert hyde["alice"].id == "alice"
        assert hyde["bob"].id == "bob"


class TestDumpLoad:
    def test_roundtrip_list_format(self, tmp_path, config):
        hyde = generate_hyde(_sections(), config, ScriptedLLM("desc"))
        path = str(tmp_path / "hyde.json")
        dump_hyde(hyde, path)
        loaded = load_hyde(path)
        assert loaded == hyde

    def test_load_dict_format(self, tmp_path):
        path = tmp_path / "hyde_dict.json"
        payload = {"alice": {"skills": ["d1", "d2"]}}
        with open(path, "w", encoding="utf-8") as f:
            json.dump(payload, f)
        loaded = load_hyde(str(path))
        assert loaded["alice"].descriptors == {"skills": ["d1", "d2"]}
