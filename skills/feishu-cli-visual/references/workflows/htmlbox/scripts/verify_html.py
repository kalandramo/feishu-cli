#!/usr/bin/env python3
"""使用隔离的 agent-browser session 验证本地 HTML，失败不得伪装成渲染成功。"""

import argparse
import json
import math
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import uuid


class BrowserError(RuntimeError):
    """依赖命令或输出异常，无法完成验证。"""


class Browser:
    def __init__(self):
        self.session = "feishu-htmlbox-" + uuid.uuid4().hex

    def run(self, *args):
        try:
            result = subprocess.run(
                ["agent-browser", "--session", self.session, "--json", *args],
                capture_output=True, text=True, timeout=60,
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise BrowserError(f"agent-browser {args[0]} 无法完成：{exc}") from exc
        if result.returncode != 0:
            raise BrowserError(f"agent-browser {args[0]} 失败（退出码 {result.returncode}）："
                               f"{result.stderr.strip() or result.stdout.strip()}")
        try:
            response = json.loads(result.stdout)
        except json.JSONDecodeError as exc:
            raise BrowserError(f"agent-browser {args[0]} 未返回合法 JSON") from exc
        if not isinstance(response, dict) or response.get("success") is not True:
            raise BrowserError(f"agent-browser {args[0]} 未成功：{response}")
        data = response.get("data")
        if not isinstance(data, dict):
            raise BrowserError(f"agent-browser {args[0]} 响应缺少 data 对象")
        return data


def validate_screenshot(path):
    """检查本次产生的 PNG，不能用先前留下的同名截图充当证据。"""
    try:
        header = path.read_bytes()[:24]
    except OSError as exc:
        raise BrowserError(f"截图未生成：{path}") from exc
    if len(header) < 24 or header[:8] != b"\x89PNG\r\n\x1a\n" or header[12:16] != b"IHDR":
        raise BrowserError(f"截图不是有效 PNG：{path}")
    if 0 in struct.unpack(">II", header[16:24]):
        raise BrowserError(f"截图尺寸无效：{path}")


def verify(html, wait_seconds):
    html = Path(html).resolve()
    if not html.is_file():
        raise BrowserError(f"文件不存在：{html}")
    if shutil.which("agent-browser") is None:
        raise BrowserError("需要安装 agent-browser 和其浏览器依赖后再验证")
    if not math.isfinite(wait_seconds) or not 0 <= wait_seconds <= 30:
        raise BrowserError("等待秒数必须在 0 到 30 之间")

    browser = Browser()
    screenshot = html.with_name(html.stem + ".verify.png")
    print(f"▶ 打开 {html.as_uri()}（独立 session: {browser.session}）")
    try:
        browser.run("open", html.as_uri())
        browser.run("wait", "--load", "domcontentloaded")
        if wait_seconds:
            browser.run("wait", str(round(wait_seconds * 1000)))
        state = browser.run("eval", "(() => ({"
                            "nodes: document.querySelectorAll('canvas,svg').length,"
                            "status: document.querySelector('#st')?.textContent.trim() || '',"
                            "url: location.href, ready: document.readyState"
                            "}))()").get("result")
        if (not isinstance(state, dict) or state.get("url") != html.as_uri()
                or state.get("ready") not in ("interactive", "complete")
                or not isinstance(state.get("nodes"), int)
                or not isinstance(state.get("status"), str)):
            raise BrowserError("无法确认当前页面已加载或渲染检查结果格式异常")
        errors = browser.run("errors").get("errors")
        messages = browser.run("console").get("messages")
        if not isinstance(errors, list) or not isinstance(messages, list):
            raise BrowserError("浏览器错误/日志响应格式异常，无法完成检查")

        with tempfile.TemporaryDirectory(prefix="feishu-htmlbox-", dir=html.parent) as tmp:
            fresh = Path(tmp) / "screenshot.png"
            browser.run("screenshot", str(fresh))
            validate_screenshot(fresh)
            fresh.replace(screenshot)

        print(f"  canvas/svg 节点数: {state['nodes']}\n  截图: {screenshot}")
        if errors:
            print("❌ 检测到 page error：" + json.dumps(errors, ensure_ascii=False), file=sys.stderr)
        bad_status = any(word in state["status"].lower() for word in ("失败", "error", "加载中"))
        if bad_status:
            print(f"❌ 状态提示异常：{state['status']}", file=sys.stderr)
        if messages:
            print("  console: " + json.dumps(messages, ensure_ascii=False))
        if errors or bad_status:
            return 1
        if state["nodes"] == 0:
            print("⚠ 无 canvas/svg；纯 CSS/KPI 可正常显示，仍需查看截图确认。")
        print("✅ 自动检查通过；请查看截图确认内容、布局，并另行确认动画行为。")
        return 0
    finally:
        # 只清理本次会话，禁止 close --all 干扰其他浏览器任务。
        try:
            browser.run("close")
        except BrowserError as exc:
            print(f"⚠ 清理独立会话失败（{browser.session}）：{exc}", file=sys.stderr)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("html", help="本地 HTML 文件")
    parser.add_argument("wait_seconds", nargs="?", type=float, default=3, help="等待渲染秒数（0-30，默认3）")
    args = parser.parse_args()
    try:
        return verify(args.html, args.wait_seconds)
    except (BrowserError, OSError) as exc:
        print(f"❌ 验证未完成：{exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
