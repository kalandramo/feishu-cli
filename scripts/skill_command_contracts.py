"""只读取 --help 校验 Markdown 命令示例；不执行示例中的业务操作。"""

from __future__ import annotations

import re
import shlex
from collections import Counter
from pathlib import Path


FLAG_RE = re.compile(r"^\s+(?:(-[A-Za-z0-9]),\s+)?(--[\w-]+)(.*)$")
VALUE_TYPES = {"string", "strings", "int", "int32", "int64", "uint", "uint64", "float", "float64", "duration", "stringArray", "stringSlice", "intSlice"}
BOUNDARIES = {"|", "||", "&&", ";", ">", ">>", "2>", "2>>"}


def parse_help(text: str) -> dict:
    flags: dict[str, bool] = {}
    aliases: list[str] = []
    section = ""
    runnable = False
    saw_usage = False
    for line in text.splitlines():
        if line and not line[0].isspace() and line.endswith(":"):
            section = line.strip()
            continue
        if section == "Usage:" and line.strip() and not saw_usage:
            runnable = "[command]" not in line
            saw_usage = True
        if section in {"Flags:", "Global Flags:"}:
            match = FLAG_RE.match(line)
            if match:
                short, long, suffix = match.groups()
                parts = suffix.split()
                takes_value = bool(parts and parts[0] in VALUE_TYPES)
                flags[long] = takes_value
                if short:
                    flags[short] = takes_value
        elif section == "Aliases:" and line.strip():
            aliases.extend(part.strip() for part in line.split(",") if part.strip())
    return {"flags": flags, "aliases": aliases, "runnable": runnable}


INCOMPLETE_SUBSTITUTION = "<incomplete-command-substitution> "
CLI_PREFIX = re.compile(r"^(?:\./)?(?:bin/)?feishu-cli(?:\s|$)")


def command_substitutions(line: str):
    """抽取真实 $() 的 CLI 内容；不把单引号内的字面示例当 shell 操作。"""
    index = 0
    quote = None
    while index < len(line):
        char = line[index]
        if char == "#" and quote is None and (index == 0 or line[index - 1].isspace()):
            return
        if char == "\\" and quote != "'":
            index += 2
            continue
        if char in ("'", '"'):
            if quote is None:
                quote = char
            elif quote == char:
                quote = None
            index += 1
            continue
        if line.startswith("$(", index) and quote != "'":
            start = index + 2
            end = start
            depth = 1
            inner_quote = None
            while end < len(line):
                value = line[end]
                if value == "\\" and inner_quote != "'":
                    end += 2
                    continue
                if value in ("'", '"'):
                    if inner_quote is None:
                        inner_quote = value
                    elif inner_quote == value:
                        inner_quote = None
                elif inner_quote is None:
                    if value == "(":
                        depth += 1
                    elif value == ")":
                        depth -= 1
                        if depth == 0:
                            break
                end += 1
            content = line[start:end].strip()
            if CLI_PREFIX.match(content):
                # 保留未闭合标记，check_example 会显式计入 skipped。
                yield content if depth == 0 else INCOMPLETE_SUBSTITUTION + content
            else:
                yield from command_substitutions(content)
            index = end + 1
            continue
        index += 1


def examples(text: str):
    """提取 fenced shell 示例与赋值/echo 中的命令替换，保留原始行号。"""
    lines = text.splitlines()
    fence = None
    index = 0
    while index < len(lines):
        line = lines[index].strip()
        number = index + 1
        index += 1
        if line.startswith(("```", "~~~")):
            marker = line[:3]
            if fence is None:
                language = line[3:].strip().lower()
                fence = (marker, language in {"", "bash", "sh", "shell", "zsh", "console"})
            elif marker == fence[0]:
                fence = None
            continue
        if not fence or not fence[1]:
            continue
        while line.endswith("\\") and index < len(lines):
            line = line[:-1] + " " + lines[index].strip()
            index += 1
        if line.startswith("$ "):
            line = line[2:]
        if CLI_PREFIX.match(line):
            yield number, line
        for command in command_substitutions(line):
            yield number, command


def check_example(line: str, catalog: dict[tuple[str, ...], dict]) -> tuple[str, list[str], str]:
    if line.startswith(INCOMPLETE_SUBSTITUTION):
        return "skipped", [], "未闭合命令替换"
    try:
        lexer = shlex.shlex(line, posix=True, punctuation_chars="|&;")
        lexer.whitespace_split = True
        tokens = list(lexer)[1:]
    except ValueError:
        return "skipped", [], "未闭合引号或多行模板"
    # 先通过 --help 声明的别名解析命令，允许全局参数位于命令前。
    path: tuple[str, ...] = ()
    index = 0
    while index < len(tokens):
        token = tokens[index]
        if token in BOUNDARIES or token == "--":
            break
        flags = catalog.get(path, {}).get("flags", {})
        flag = token.split("=", 1)[0]
        if flag in flags:
            index += 1 + int(flags[flag] and "=" not in token)
            continue
        direct = path + (token,)
        if direct in catalog:
            path = direct
            index += 1
            continue
        alias = next((candidate for candidate, info in catalog.items()
                      if candidate[:-1] == path and token in info.get("aliases", [])), None)
        if alias is not None:
            path = alias
            index += 1
            continue
        if not path and re.fullmatch(r"--[\w-]+(?:=.*)?", token):
            return "checked", [flag], "<root>"
        break
    if not path and index < len(tokens):
        if any(char in tokens[index] for char in "${}<[]") or tokens[index] == "...":
            return "skipped", [], "动态根命令模板"
        return "invalid", [f"不存在的子命令 {tokens[index]}"], "<root>"
    # foo {get|list} / $ACTION 等不是可执行示例，不能拿父组参数误判子命令。
    if index < len(tokens) and any(char in tokens[index] for char in "${}<[]"):
        if not tokens[index].startswith("--") and any(candidate[:-1] == path for candidate in catalog):
            return "skipped", [], "动态子命令模板"
    if (index < len(tokens) and tokens[index] not in BOUNDARIES and not tokens[index].startswith("-")
            and any(candidate[:-1] == path for candidate in catalog if candidate)
            and not catalog.get(path, {}).get("runnable", False)):
        return "invalid", [f"不存在的子命令 {tokens[index]}"], " ".join(path) or "<root>"
    flags = catalog[path]["flags"]
    unknown: list[str] = []
    index = 0
    while index < len(tokens):
        token = tokens[index]
        if token in BOUNDARIES or token == "--":
            break
        flag = token.split("=", 1)[0]
        if flag in flags:
            index += 1 + int(flags[flag] and "=" not in token)
            continue
        if re.fullmatch(r"--[\w-]+(?:=.*)?", token):
            unknown.append(flag)
        index += 1
    return "checked", sorted(set(unknown)), " ".join(path)


def check_examples(paths: list[Path], catalog: dict, root: Path) -> tuple[list[str], dict]:
    errors: list[str] = []
    checked = 0
    skipped: Counter = Counter()
    for path in sorted(paths):
        for number, line in examples(path.read_text(encoding="utf-8")):
            status, unknown, detail = check_example(line, catalog)
            if status == "skipped":
                skipped[detail] += 1
                continue
            checked += 1
            if status == "invalid":
                errors.append(f"{path.relative_to(root)}:{number} {detail} 命令无效: {', '.join(unknown)}")
            elif unknown:
                errors.append(f"{path.relative_to(root)}:{number} {detail} 使用不存在的长参数: {', '.join(unknown)}")
    return errors, {"checked": checked, "skipped": sum(skipped.values()), "skip_reasons": dict(skipped)}
