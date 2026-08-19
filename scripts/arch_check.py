"""依赖边界检查（Makefile `make arch` 目标）。

规则（对应 spec/02-stages.md 的分层：core 纯变换 / adapter 负责 IO）：

- 入口/适配层（cli / runners / bench / store）不得被 pipeline 核心模块 import。
  核心 = schemas / config / extract / hyde / embed / similarity / select / score /
  match / introduce / report / evaluate / llm / surrogate / feedback / stages。

违规即 exit 1（CI `check` 门禁的一部分）。
"""

from __future__ import annotations

import ast
import sys
from pathlib import Path

# 入口/适配层模块（可被外部调用者使用，但不得反向进入核心层依赖图）
ENTRY_MODULES = {"cli", "runners", "bench", "store"}

ROOT = Path(__file__).resolve().parents[1] / "src" / "mutual"


def internal_imports(tree: ast.AST) -> set[str]:
    """收集全部内部依赖形式（qodo #7：补上普通 import 语句）。

    覆盖四种写法：
    - ``from .x import ...`` / ``from . import x``
    - ``from mutual.x import ...`` / ``from mutual import x``
    - ``import mutual.x`` / ``import mutual.x as y``（此前漏检）
    - ``import mutual``
    """
    found: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.ImportFrom):
            mod = node.module or ""
            if node.level > 0:  # from .x import y / from . import y
                found.add(mod.split(".")[0] if mod else "")
                for alias in node.names:  # from . import x
                    found.add(alias.name.split(".")[0])
            elif mod == "mutual" or mod.startswith("mutual."):
                parts = mod.split(".")
                if len(parts) > 1:
                    found.add(parts[1])
                else:  # from mutual import x
                    for alias in node.names:
                        found.add(alias.name.split(".")[0])
        elif isinstance(node, ast.Import):
            for alias in node.names:
                name = alias.name
                if name == "mutual":
                    continue  # import mutual 本身未指名子模块
                if name.startswith("mutual."):
                    found.add(name.split(".")[1])  # import mutual.cli → cli
    return {m for m in found if m}


def main() -> int:
    offenders: list[str] = []
    for py in sorted(ROOT.glob("*.py")):
        module = py.stem
        if module in ENTRY_MODULES or module == "__init__":
            continue
        tree = ast.parse(py.read_text(encoding="utf-8"))
        bad = internal_imports(tree) & ENTRY_MODULES
        for dep in sorted(bad):
            offenders.append(f"src/mutual/{module}.py -> mutual.{dep}")
    if offenders:
        print("::error::核心模块禁止 import 入口/适配层（cli/runners/bench/store）：")
        for o in offenders:
            print(f"  {o}")
        return 1
    print(f"OK 依赖边界（{len(list(ROOT.glob('*.py')))} 模块，入口层单向依赖）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
