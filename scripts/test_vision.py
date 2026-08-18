#!/usr/bin/env python3
"""Loadout 视觉能力测试脚本。

用法:
    python test_vision.py <图片路径> [更多图片...] [选项]

示例:
    python test_vision.py ./cat.png
    python test_vision.py ./a.png ./b.png --model deepseek-chat
    python test_vision.py ./cat.png --no-stream
    python test_vision.py ./cat.png --api-key sk-xxxx --prompt "图片里有什么动物？"

前提（三个都要满足，否则视觉不会被触发）:
    1. Loadout 已启动，默认监听 http://127.0.0.1:3000
    2. 在后台「密钥」页签发一个 sk- key（脚本用 --api-key 或环境变量 LOADOUT_API_KEY 传入）
    3. 在后台「渠道 / 模型」→「能力路由（视觉附加）」给目标模型配好 vision=proxy 路由，
       例如：目标模型 deepseek-chat → proxy → via_model=qwen-vl-max（视觉渠道选你的渠道）

原理:
    脚本把本地图片转成 base64 data URI 放进 image_url 分段，发给 Loadout；
    Loadout 的 vision 插件会拦截图片、调视觉模型生成文字描述、再转发给主模型。
    默认流式输出：先打印视觉描述（reasoning 块），再打印主模型的流式回答；
    用 --no-stream 关闭流式，只打印主模型的最终回答。
"""

import argparse
import base64
import json
import mimetypes
import sys
import urllib.error
import urllib.request

DEFAULT_BASE_URL = "http://127.0.0.1:3000"
DEFAULT_MODEL = "deepseek-chat"
DEFAULT_PROMPT = "请描述这张图片的内容"


def ensure_utf8_stdout():
    """Windows 控制台默认 GBK，强制 stdout 用 UTF-8，避免中文乱码。"""
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, "reconfigure"):
            try:
                stream.reconfigure(encoding="utf-8")
            except Exception:
                pass


def image_data_uri(path: str) -> str:
    """把本地图片转成 data URI（base64）。"""
    try:
        with open(path, "rb") as f:
            raw = f.read()
    except OSError as e:
        sys.exit(f"无法读取图片 {path!r}: {e}")

    mime, _ = mimetypes.guess_type(path)
    if mime is None:
        mime = "image/png"
    return f"data:{mime};base64," + base64.b64encode(raw).decode()


def build_payload(model: str, data_uris: list[str], prompt: str, stream: bool) -> dict:
    content = [
        {"type": "image_url", "image_url": {"url": uri}} for uri in data_uris
    ]
    content.append({"type": "text", "text": prompt})
    return {
        "model": model,
        "messages": [{"role": "user", "content": content}],
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
        description="Loadout 视觉能力测试：给不支持视觉的模型附加视觉能力",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("images", nargs="+", help="一个或多个本地图片路径")
    parser.add_argument("--model", default=DEFAULT_MODEL,
                        help=f"目标模型（默认 {DEFAULT_MODEL}，需配 vision=proxy 路由）")
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL,
                        help=f"Loadout 地址（默认 {DEFAULT_BASE_URL}）")
    parser.add_argument("--api-key", default=None,
                        help="sk- key（或用环境变量 LOADOUT_API_KEY）")
    parser.add_argument("--prompt", default=DEFAULT_PROMPT,
                        help=f"附加文字提示（默认“{DEFAULT_PROMPT}”）")
    parser.add_argument("--stream", action=argparse.BooleanOptionalAction,
                        default=True,
                        help="流式输出（默认开启；--no-stream 关闭，只打印主模型最终回答）")
    parser.add_argument("--timeout", type=int, default=120, help="请求超时秒数（默认 120）")
    args = parser.parse_args()

    api_key = args.api_key or __import__("os").environ.get("LOADOUT_API_KEY")
    if not api_key:
        sys.exit("缺少 sk- key：请用 --api-key 传入，或设置环境变量 LOADOUT_API_KEY")
    # 去掉粘贴时混入的空白/换行（sk key 本身不含空白）。
    api_key = "".join(api_key.split())

    data_uris = [image_data_uri(p) for p in args.images]
    print(f"目标模型: {args.model}")
    print(f"图片: {', '.join(args.images)} 共 {len(args.images)} 张")
    print(f"模式: {'流式' if args.stream else '非流式'}\n")

    payload = build_payload(args.model, data_uris, args.prompt, args.stream)
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
