"""Mutual — Stage 注册测试。

验证 11 个 stage 全部注册、describe_stage 返回可读描述。
这些测试确保 spec 的"可执行单元"完整。
"""

import pytest

from mutual.stages import (
    StageSpec,
    describe_all,
    describe_stage,
    get_stage,
    list_stages,
    register,
)

EXPECTED_STAGES = [
    "extract",
    "hyde",
    "embed",
    "similarity",
    "select",
    "score",
    "pre_matrix",
    "match",
    "introduce",
    "report",
    "evaluate",
]

# Phase 1 已实现（run 替换为真函数）的 stage；其余仍为 Phase 0 stub。
IMPLEMENTED_STAGES = ["score", "pre_matrix", "introduce", "report"]
STILL_STUB_STAGES = [s for s in EXPECTED_STAGES if s not in IMPLEMENTED_STAGES]


class TestRegistry:
    def test_all_stages_registered(self):
        stages = list_stages()
        for name in EXPECTED_STAGES:
            assert name in stages, f"Stage '{name}' not registered"

    def test_stage_count(self):
        assert len(list_stages()) == 11

    def test_duplicate_register_raises(self):
        with pytest.raises(ValueError, match="already registered"):
            register(
                StageSpec(
                    name="extract",
                    description="dup",
                    input_schema={},
                    output_schema="",
                    run=lambda: None,
                )
            )


class TestDescribeStage:
    def test_describe_returns_required_fields(self):
        d = describe_stage("score")
        assert "name" in d
        assert "description" in d
        assert "input_schema" in d
        assert "output_schema" in d
        assert "notes" in d

    def test_describe_unknown_stage_raises(self):
        with pytest.raises(KeyError):
            describe_stage("nonexistent")

    def test_describe_all_count(self):
        all_descs = describe_all()
        assert len(all_descs) == 11


class TestStageContracts:
    """验证每个 stage 的 IO 契约字段存在。"""

    @pytest.mark.parametrize("stage_name", EXPECTED_STAGES)
    def test_stage_has_input_schema(self, stage_name):
        spec = get_stage(stage_name)
        assert spec.input_schema, f"Stage '{stage_name}' has empty input_schema"
        assert isinstance(spec.input_schema, dict)

    @pytest.mark.parametrize("stage_name", EXPECTED_STAGES)
    def test_stage_has_output_schema(self, stage_name):
        spec = get_stage(stage_name)
        assert spec.output_schema, f"Stage '{stage_name}' has empty output_schema"

    @pytest.mark.parametrize("stage_name", EXPECTED_STAGES)
    def test_stage_has_description(self, stage_name):
        spec = get_stage(stage_name)
        assert spec.description, f"Stage '{stage_name}' has empty description"

    @pytest.mark.parametrize("stage_name", EXPECTED_STAGES)
    def test_stage_has_notes(self, stage_name):
        spec = get_stage(stage_name)
        assert spec.notes, (
            f"Stage '{stage_name}' has empty notes — every stage must document its boundaries"
        )


IMPLEMENTED_STAGES = [
    "extract",
    "hyde",
    "embed",
    "similarity",
    "select",
    "score",
    "pre_matrix",
    "match",
    "introduce",
    "report",
    "evaluate",
]
STILL_STUB_STAGES = [s for s in EXPECTED_STAGES if s not in IMPLEMENTED_STAGES]


class TestStubBehavior:
    """Phase 2：全部 11 个 stage 均已实现为真函数，无 Phase 0 stub。"""

    @pytest.mark.parametrize("stage_name", STILL_STUB_STAGES)
    def test_stub_raises(self, stage_name):
        spec = get_stage(stage_name)
        with pytest.raises(NotImplementedError, match="Phase 0"):
            spec.run()

    @pytest.mark.parametrize("stage_name", IMPLEMENTED_STAGES)
    def test_implemented_stage_is_real_function(self, stage_name):
        """已实现 stage 的 run 不再是 _stub_run（无参调用报 TypeError 而非 stub 提示）。"""
        spec = get_stage(stage_name)
        assert getattr(spec.run, "__name__", "") != "_stub_run"
        with pytest.raises(TypeError):
            spec.run()

    @pytest.mark.parametrize("stage_name", ["extract", "hyde", "embed"])
    def test_implemented_stage_has_load_dump(self, stage_name):
        spec = get_stage(stage_name)
        assert spec.load is not None
        assert spec.dump is not None
