#!/usr/bin/env python3
"""Loadout 纯文本对话测试脚本（无需图片，可直接运行）。

用法:
    python test_chat.py
    python test_chat.py --prompt "用一句话介绍你自己"
    python test_chat.py --model deepseek-chat --no-stream
    python test_chat.py --api-key sk-xxxx --prompt "你好"

前提:
    1. Loadout 已启动，默认监听 http://127.0.0.1:3000
    2. 在后台「密钥」页签发一个 sk- key（用 --api-key 或环境变量 LOADOUT_API_KEY 传入）

与 test_vision.py 的区别：本脚本只发文字、不附带图片，
不依赖视觉路由（vision=proxy），可对普通文本模型直接发起对话测试。
所有参数都带默认值 —— 只要配好 key，就能「直接运行」。
python scripts/test_chat.py --api-key sk-bd0c4180494c89b4deb16d02d556fb2b0fc63ab66cef4de2e0c07166c0f94e86 --prompt "你好" --model hy3
python scripts/test_chat.py --base-url https://imohuan.shop --api-key sk-f8582a7d35f6dd0b80cff10990324a586f6ba3a01b7a1798390836a20b951259 --prompt "你好" --model hy3
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

DEFAULT_BASE_URL = "http://127.0.0.1:3000"
DEFAULT_MODEL = "deepseek-chat"
DEFAULT_PROMPT = "你好，请用一句话介绍你自己"


def ensure_utf8_stdout():
    """Windows 控制台默认 GBK，强制 stdout 用 UTF-8，避免中文乱码。"""
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, "reconfigure"):
            try:
                stream.reconfigure(encoding="utf-8")
            except Exception:
                pass


def build_payload(model: str, prompt: str, stream: bool) -> dict:
    """只构造纯文字消息，不拼 image_url 分段。"""
    return {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "stream": stream,
    }


def do_request(base_url: str, api_key: str, payload: dict, timeout: int):
    url = base_url.rstrip("/") + "/v1/chat/completions"
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {api_key}",
    }
    req = urllib.request.Request(
        url, data=json.dumps(payload).encode("utf-8"), headers=headers, method="POST"
    )
    return urllib.request.urlopen(req, timeout=timeout)


def print_non_stream(resp):
    body = json.loads(resp.read().decode("utf-8"))
    try:
        content = body["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError):
        print(json.dumps(body, ensure_ascii=False, indent=2))
        sys.exit("响应里没有 choices[0].message.content，完整响应已打印如上。")
    print(content)


def print_stream(resp):
    """流式输出：原样透传服务端返回的 SSE event（data: 行），不解析、不加前缀。"""
    for raw_line in resp:
        line = raw_line.decode("utf-8", errors="replace").rstrip("\r\n")
        if line.startswith("data:"):
            print(line, flush=True)


def main():
    ensure_utf8_stdout()

    parser = argparse.ArgumentParser(
        description="Loadout 纯文本对话测试（无需图片，可直接运行）",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("--model", default=DEFAULT_MODEL,
                        help=f"目标模型（默认 {DEFAULT_MODEL}）")
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL,
                        help=f"Loadout 地址（默认 {DEFAULT_BASE_URL}）")
    parser.add_argument("--api-key", default=None,
                        help="sk- key（或用环境变量 LOADOUT_API_KEY）")
    parser.add_argument("--prompt", default=DEFAULT_PROMPT,
                        help=f"对话提示（默认“{DEFAULT_PROMPT}”）")
    parser.add_argument("--stream", action=argparse.BooleanOptionalAction,
                        default=True,
                        help="流式输出（默认开启；--no-stream 关闭，只打印最终回答）")
    parser.add_argument("--timeout", type=int, default=120, help="请求超时秒数（默认 120）")
    args = parser.parse_args()

    api_key = args.api_key or os.environ.get("LOADOUT_API_KEY")
    if not api_key:
        sys.exit("缺少 sk- key：请用 --api-key 传入，或设置环境变量 LOADOUT_API_KEY")
    # 去掉粘贴时混入的空白/换行（sk key 本身不含空白）。
    api_key = "".join(api_key.split())

    print(f"目标模型: {args.model}")
    print(f"提示: {args.prompt}")
    print(f"模式: {'流式' if args.stream else '非流式'}\n")

    payload = build_payload(args.model, args.prompt, args.stream)
    try:
        with do_request(args.base_url, api_key, payload, args.timeout) as resp:
            if args.stream:
                print_stream(resp)
            else:
                print_non_stream(resp)
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        try:
            msg = json.loads(body).get("error", {}).get("message", body)
        except json.JSONDecodeError:
            msg = body
        sys.exit(f"HTTP {e.code}: {msg}")
    except urllib.error.URLError as e:
        sys.exit(f"无法连接 Loadout（{args.base_url}）: {e.reason}")


if __name__ == "__main__":
    main()
