#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
音频理解直测脚本（方舟 Ark Chat API）
=====================================
用途：绕开 loadout 网关，直接用方舟 chat/completions 接口测试音频理解，
      用于验证插件修复后请求格式是否正确（input_audio.data 纯 base64 + format）。

依赖：Python 3 标准库（urllib），无第三方包。

用法：
    ARK_API_KEY=你的key python 测试目录/test_audio_recognition.py \
        --audio "F:\\其他\\562d3584-8ce1-448a-9724-27ed929f758f.wav" \
        --model doubao-seed-evolving \
        --task asr

参数：
    --audio    音频文件路径（本地，转纯 base64）或 http(s) URL（走 url 字段）
    --model    模型 ID，默认 doubao-seed-evolving
    --task     识别模式：asr(转写)/timed(时间戳)/diarize(说话人)/translate(翻译)/caption(分析)，默认 asr
    --base-url 方舟端点，默认 https://ark.cn-beijing.volces.com/api/v3
"""

import argparse
import base64
import json
import os
import sys
import urllib.request
import urllib.error

DEFAULT_BASE_URL = "https://ark.cn-beijing.volces.com/api/v3"


def audio_format(path: str) -> str:
    """按扩展名推断 format（方舟 Chat API 的 input_audio.format 取值）。"""
    ext = os.path.splitext(path)[1].lower()
    return {
        ".mp3": "mp3",
        ".wav": "wav",
        ".aac": "aac",
        ".m4a": "m4a",
        ".ogg": "ogg",
        ".flac": "flac",
    }.get(ext, "mp3")


def build_input_audio(audio: str):
    """构造 input_audio 块：本地文件转纯 base64(data)，URL 走 url 字段。"""
    fmt = audio_format(audio) if not audio.lower().startswith(("http://", "https://")) else "mp3"
    ia = {"format": fmt}
    if audio.lower().startswith(("http://", "https://")):
        ia["url"] = audio
    else:
        with open(audio, "rb") as f:
            data = base64.b64encode(f.read()).decode("ascii")
        ia["data"] = data
    return ia


def user_prompt(task: str) -> str:
    """按 task 生成默认用户提示（与插件 audioUserPrompt 对齐）。"""
    task = (task or "asr").lower()
    return {
        "asr": "请识别音频中的内容，以文字形式返回识别结果。",
        "timed": "请转录这段音频文件。对于识别出的每一个字请提供其精确的开始时间和结束时间。\n你需要按着一字一行的格式来排列结果，每一行用';'隔开。每一行由三部分组成，分别为开始时间、结束时间、转写字符，并且用'-'将它们分割开。要注意开始时间和结束时间的单位为秒，可以精确到小数点后两位。\n可以参考下面的模板：\n{开始时间}-{结束时间}-{转写字符};{开始时间}-{结束时间}-{转写字符};...\n注意你只能按着模板输出结果，请勿输出其它无关的信息和内容。",
        "diarize": "请顺序输出说话人编号以及语音内容，并为每段发言标注起止时间。可参考模板：[spk0][开始-结束]说话内容...",
        "translate": "把这句话翻译成目标语言，最终输出仅能是翻译结果，不要返回任何其他多余的内容。",
        "caption": "请整体描述这段音频，按markdown格式输出。\n# 内容要求\n### 音频概述\n整体概述音频的物理属性（比如时长、音色音量、清晰度），核心内容构成，整体听感；\n### 内容分析(如有)\n概括对话或独白的主要内容发展，总结标题和摘要\n### 说话人信息(如有)\n对音频说话部分进行说话人语音特征分析\n### 声音事件信息\n对音频非言语部分进行音频特征分析\n### 音乐信息\n对音频音乐部分进行音频特征分析",
    }.get(task, "请识别音频中的内容，以文字形式返回识别结果。")


def main():
    ap = argparse.ArgumentParser(description="方舟音频理解直测")
    ap.add_argument("--audio", required=True, help="音频文件路径或 http(s) URL")
    ap.add_argument("--model", default="doubao-seed-evolving", help="模型 ID")
    ap.add_argument("--task", default="asr",
                    choices=["asr", "timed", "diarize", "translate", "caption"])
    ap.add_argument("--base-url", default=DEFAULT_BASE_URL)
    args = ap.parse_args()

    api_key = os.environ.get("ARK_API_KEY", "").strip()
    if not api_key:
        print("错误: 未设置环境变量 ARK_API_KEY（方舟 API Key）。", file=sys.stderr)
        print("用法: ARK_API_KEY=你的key python 测试目录/test_audio_recognition.py --audio <音频>", file=sys.stderr)
        sys.exit(2)

    if not args.audio.lower().startswith(("http://", "https://")) and not os.path.exists(args.audio):
        print(f"错误: 音频文件不存在: {args.audio}", file=sys.stderr)
        sys.exit(2)

    try:
        input_audio = build_input_audio(args.audio)
    except OSError as e:
        print(f"错误: 读取音频失败: {e}", file=sys.stderr)
        sys.exit(2)

    body = {
        "model": args.model,
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "input_audio", "input_audio": input_audio},
                    {"type": "text", "text": user_prompt(args.task)},
                ],
            }
        ],
        "stream": False,
    }
    payload = json.dumps(body).encode("utf-8")

    url = args.base_url.rstrip("/") + "/chat/completions"
    req = urllib.request.Request(url, data=payload, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", f"Bearer {api_key}")

    # 打印请求摘要（data 只显示长度，避免刷屏）
    data_preview = (input_audio.get("data") or input_audio.get("url") or "")
    if "data" in input_audio:
        data_preview = f"<纯base64, {len(data_preview)}字符, 对应音频 {os.path.getsize(args.audio)}B>"
    print("==== 请求 ====")
    print(f"URL     : {url}")
    print(f"model   : {args.model}")
    print(f"task    : {args.task}")
    print(f"audio块 : type=input_audio, format={input_audio['format']}, {data_preview}")
    print()

    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            resp_body = resp.read().decode("utf-8")
            print(f"HTTP {resp.status}")
            print("==== 响应 ====")
            print(resp_body)
    except urllib.error.HTTPError as e:
        print(f"HTTP {e.code}", file=sys.stderr)
        print(e.read().decode("utf-8", "replace"), file=sys.stderr)
        sys.exit(1)
    except urllib.error.URLError as e:
        print(f"网络错误: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
