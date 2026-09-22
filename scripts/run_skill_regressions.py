#!/usr/bin/env python3
"""发现并运行各领域的离线脚本回归，再执行新编译 CLI 的本地行为契约。"""

from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path, help="当前源码构建的 feishu-cli")
    args = parser.parse_args()
    directories = {ROOT / "scripts"}
    for path in (ROOT / "skills").rglob("test_*.py"):
        relative = path.relative_to(ROOT / "skills")
        if not relative.parts[0].endswith("-workspace"):
            directories.add(path.parent)
    failures = []
    for directory in sorted(directories):
        label = str(directory.relative_to(ROOT))
        print(f"\n[脚本行为回归] {label}", flush=True)
        proc = subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", str(directory), "-p", "test_*.py"], cwd=ROOT, check=False)
        if proc.returncode:
            failures.append(label)
    print("\n[二进制离线行为契约] 参数拒绝、dry-run 与本地 HTTP 请求捕获", flush=True)
    proc = subprocess.run([sys.executable, str(ROOT / "scripts/check_skill_contracts.py"), str(args.binary.resolve())], cwd=ROOT, check=False)
    if proc.returncode:
        failures.append("二进制离线契约")
    if failures:
        print("Skill 行为回归失败: " + ", ".join(failures), file=sys.stderr)
        return 1
    print(f"Skill 行为回归通过: {len(directories)} 组脚本测试 + 二进制离线契约。未运行模型触发评测或线上业务测试。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
