#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""脏 assistant 消息拦截测试

用法：
    python scripts/test_dirty_message.py --base-url http://127.0.0.1:3000 --api-key sk-xxx

    # 原封不动重放一份 request-log 导出的 body（完整 model/messages/tools/stream_options 等）：
    python scripts/test_dirty_message.py --replay body.json --base-url ... --api-key ...

构造一条含「assistant 空 content + 空 tool_call name」的脏消息发给网关：
  - HTTP 200            -> 拦截生效（网关剥掉脏消息后上游接受）
  - HTTP 400 code 11133 -> 拦截未生效（脏消息直达上游被拒）
"""
import argparse
import json
import sys
import urllib.request
import urllib.error

if sys.platform == "win32":
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")


def send(base_url, api_key, body_bytes, label):
    url = base_url.rstrip("/") + "/v1/chat/completions"
    print(f"目标: {url}")
    print(f"场景: {label}")
    req = urllib.request.Request(
        url,
        data=body_bytes,
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            status = resp.status
            text = resp.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as e:
        status = e.code
        text = e.read().decode("utf-8", errors="replace")
    except Exception as e:
        print(f"请求失败: {e}")
        sys.exit(1)
    print(f"HTTP 状态: {status}")
    preview = text[:300] + ("..." if len(text) > 300 else "")
    print(f"响应预览: {preview}")
    return status, text


def report(status, text):
    if status == 200:
        print("PASS: 上游正常接受（脏消息已被网关拦截/剥除）")
        sys.exit(0)
    if status == 400 and "11133" in text:
        print("FAIL: 脏消息到达上游被拒（code 11133），拦截未生效")
        sys.exit(2)
    print("WARN: 其他状态，请人工检查")
    sys.exit(3)


def main():
    parser = argparse.ArgumentParser(description="脏 assistant 消息拦截测试")
    parser.add_argument("--base-url", default="http://127.0.0.1:3000")
    parser.add_argument("--api-key", required=True)
    parser.add_argument("--model", default="step-5-preview")
    parser.add_argument("--stream", action="store_true", help="以 stream 模式发送")
    parser.add_argument("--replay", help="request-log 导出的 body JSON 文件路径（原封不动重放）")
    args = parser.parse_args()

    if args.replay:
        with open(args.replay, "rb") as f:
            raw = f.read()
        # request-log 的 request_json.body 是「网关处理后」的字符串；可能本身是 JSON 字符串（双重编码），
        # 兼容两种：文件内容直接是 body JSON，或是包着 body 字符串的 JSON。
        try:
            parsed = json.loads(raw)
            if isinstance(parsed, str):
                body_bytes = parsed.encode("utf-8")
            else:
                body_bytes = raw
        except json.JSONDecodeError:
            body_bytes = raw
        b = json.loads(body_bytes)
        msgs = b.get("messages", [])
        def content_empty(m):
            c = m.get("content")
            if c is None:
                return True
            if isinstance(c, str):
                return not c.strip()
            if isinstance(c, list):
                return len(c) == 0
            return False

        dirty = [
            i for i, m in enumerate(msgs)
            if m.get("role") == "assistant"
            and content_empty(m)
            and any(not (tc.get("function") or {}).get("name") for tc in (m.get("tool_calls") or []))
        ]
        print(f"重放文件: {args.replay}")
        print(f"body 大小: {len(body_bytes)} 字节  消息数: {len(msgs)}  脏 assistant 位置: {dirty or '无'}")
        if b.get("tools"):
            print(f"tools: {len(b['tools'])} 个  stream: {b.get('stream')}")
        status, text = send(args.base_url, args.api_key, body_bytes, "原封不动重放 request-log 的 body")
        report(status, text)

    # 最小构造场景
    body = {
        "model": args.model,
        "stream": args.stream,
        "messages": [
            {"role": "user", "content": "回复：OK"},
            {
                "content": "",
                "role": "assistant",
                "tool_calls": [
                    {
                        "function": {"arguments": "{}", "name": ""},
                        "id": "chatcmpl-tool-949af1e6b71bd6fd",
                        "type": "function",
                    }
                ],
            },
        ],
    }
    data = json.dumps(body).encode("utf-8")
    print(f"模式: stream={args.stream}  消息数: {len(body['messages'])}（含 1 条脏 assistant）")
    print(f"发送的 body:\n{json.dumps(body, ensure_ascii=False, indent=2)}")
    status, text = send(args.base_url, args.api_key, data, "最小构造脏消息")
    report(status, text)


if __name__ == "__main__":
    main()

