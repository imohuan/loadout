#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
豆包语音 · 有声书批量合成脚本
==============================
输入一本 txt 小说，自动按段落切分 → 逐段调用豆包语音合成大模型 2.0 →
生成 mp3 → 拼接成一本完整有声书。

依赖：仅 Python 标准库（无需 pip 安装任何东西）。
可选：装了 ffmpeg 可启用段间静音间隔；没装也能二进制拼接 mp3。

环境变量（二选一配置密钥，推荐新版 API Key）：
  新版控制台（API Key 管理）：
      export DOUBAO_TTS_API_KEY=your-api-key
  旧版控制台（应用详情 AppID + Access Token）：
      export DOUBAO_TTS_APPID=your-app-id
      export DOUBAO_TTS_TOKEN=your-access-token

用法示例：
  python audiobook_tts.py 小说.txt -o 小说.mp3 \
      --speaker zh_female_vv_uranus_bigtts \
      --max-chars 400 --speed 0 --gap 0.3

参数说明：
  --speaker     音色ID（控制台>音色库 试听获取），必填
  --max-chars   每段最大字数，默认 400，长文本自动按句切分
  --speed       语速 [-50,100]，0 为原速
  --loudness    音量 [-50,100]，0 为原始音量
  --gap         段与段之间静音秒数，默认 0（需要 ffmpeg）
  --sample-rate 采样率，默认 24000
  --keep        保留中间分段音频（默认拼接后删除）
"""

import os
import sys
import json
import uuid
import time
import base64
import argparse
import urllib.request
import urllib.error

API_URL = "https://openspeech.bytedance.com/api/v3/tts/unidirectional"
RESOURCE_ID = "seed-tts-2.0"


# ---------------------------------------------------------------- 文本切分
def read_text(path):
    for enc in ("utf-8", "utf-8-sig", "gbk", "gb18030"):
        try:
            with open(path, "r", encoding=enc) as f:
                return f.read()
        except (UnicodeDecodeError, LookupError):
            continue
    raise SystemExit(f"无法识别文件编码: {path}")


def split_text(text, max_chars):
    """按换行拆句，再把短句合并成不超过 max_chars 的段落。"""
    raw = [s.strip() for s in text.splitlines() if s.strip()]
    segments = []
    buf = ""
    for line in raw:
        # 单句超过上限，硬切
        while len(line) > max_chars:
            if buf:
                segments.append(buf)
                buf = ""
            segments.append(line[:max_chars])
            line = line[max_chars:]
        if len(buf) + len(line) + 1 > max_chars:
            if buf:
                segments.append(buf)
            buf = line
        else:
            buf = f"{buf}\n{line}" if buf else line
    if buf:
        segments.append(buf)
    return segments


# ---------------------------------------------------------------- 密钥配置
def build_headers():
    api_key = os.environ.get("DOUBAO_TTS_API_KEY")
    appid = os.environ.get("DOUBAO_TTS_APPID")
    token = os.environ.get("DOUBAO_TTS_TOKEN")
    headers = {
        "X-Api-Resource-Id": RESOURCE_ID,
        "X-Api-Request-Id": str(uuid.uuid4()),
        "Content-Type": "application/json",
    }
    if api_key:
        headers["X-Api-Key"] = api_key
        return headers
    if appid and token:
        headers["X-Api-App-Id"] = appid
        headers["X-Api-Access-Key"] = token
        return headers
    raise SystemExit(
        "未配置密钥。请设置环境变量：\n"
        "  新版: DOUBAO_TTS_API_KEY\n"
        "  旧版: DOUBAO_TTS_APPID + DOUBAO_TTS_TOKEN"
    )


# ---------------------------------------------------------------- 调用接口
def synth_one(headers, text, speaker, speed, loudness, sample_rate):
    body = {
        "req_params": {
            "text": text,
            "speaker": speaker,
            "audio_params": {
                "format": "mp3",
                "sample_rate": sample_rate,
                "speech_rate": speed,
                "loudness_rate": loudness,
            },
        }
    }
    req = urllib.request.Request(
        API_URL,
        data=json.dumps(body).encode("utf-8"),
        headers=headers,
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=120) as resp:
        raw = resp.read().decode("utf-8")
    # 流式接口可能整体返回一个 JSON，也可能按行返回多段 JSON
    chunks = []
    for line in raw.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if obj.get("code") not in (0, None):
            raise RuntimeError(f"接口错误 code={obj.get('code')} msg={obj.get('message')}")
        d = obj.get("data")
        if d:
            chunks.append(d)
    if not chunks:
        raise RuntimeError("接口未返回音频数据")
    return base64.b64decode("".join(chunks))


def synth_with_retry(headers, text, speaker, speed, loudness, sample_rate, retries=3):
    for i in range(retries):
        try:
            return synth_one(headers, text, speaker, speed, loudness, sample_rate)
        except Exception as e:  # noqa: BLE001
            if i == retries - 1:
                raise
            print(f"    重试 {i + 1}/{retries - 1} ... ({e})")
            time.sleep(2 * (i + 1))
    raise RuntimeError("unreachable")


# ---------------------------------------------------------------- 拼接
def ffmpeg_available():
    import shutil
    return shutil.which("ffmpeg") is not None


def concat_with_ffmpeg(files, out, gap):
    import subprocess
    import tempfile
    if gap > 0:
        # 每段之间插静音，需要重编码
        inputs = []
        for f in files:
            inputs += ["-i", f]
        filter_parts = []
        for i in range(len(files)):
            filter_parts.append(f"[{i}:a]")
            if i < len(files) - 1:
                filter_parts.append(f"anullsrc=r=24000:cl=mono:d={gap}[s{i}]")
                filter_parts.append(f"[s{i}]")
        cmd = ["ffmpeg", "-y"] + inputs + [
            "-filter_complex",
            "".join(filter_parts) + f"concat=n={len(files) * 2 - 1}:v=0:a=1[out]",
            "-map", "[out]", "-b:a", "128k", out,
        ]
    else:
        # 直接 copy 流拼接，快且无损
        with tempfile.NamedTemporaryFile(
            "w", suffix=".txt", delete=False, encoding="utf-8"
        ) as f:
            for p in files:
                f.write(f"file '{os.path.abspath(p)}'\n")
            listfile = f.name
        cmd = [
            "ffmpeg", "-y", "-f", "concat", "-safe", "0",
            "-i", listfile, "-c", "copy", out,
        ]
        try:
            subprocess.run(cmd, check=True, capture_output=True)
        finally:
            os.unlink(listfile)
        return
    subprocess.run(cmd, check=True, capture_output=True)


def concat_binary(files, out):
    """无 ffmpeg 时的兜底：mp3 帧流直接二进制拼接，绝大多数播放器可正常播放。"""
    with open(out, "wb") as dst:
        for p in files:
            with open(p, "rb") as src:
                dst.write(src.read())


# ---------------------------------------------------------------- 主流程
def main():
    ap = argparse.ArgumentParser(description="豆包语音有声书批量合成")
    ap.add_argument("input", help="输入 txt 文件路径")
    ap.add_argument("-o", "--output", default="audiobook.mp3", help="输出 mp3 路径")
    ap.add_argument("--speaker", required=True, help="音色ID，例如 zh_female_vv_uranus_bigtts")
    ap.add_argument("--max-chars", type=int, default=400, help="每段最大字数")
    ap.add_argument("--speed", type=int, default=0, help="语速 [-50,100]")
    ap.add_argument("--loudness", type=int, default=0, help="音量 [-50,100]")
    ap.add_argument("--sample-rate", type=int, default=24000)
    ap.add_argument("--gap", type=float, default=0.0, help="段间静音秒数(需 ffmpeg)")
    ap.add_argument("--keep", action="store_true", help="保留分段音频")
    args = ap.parse_args()

    headers = build_headers()
    text = read_text(args.input)
    segments = split_text(text, args.max_chars)
    if not segments:
        raise SystemExit("文本为空或只有空白")
    print(f"共切分 {len(segments)} 段，音色 {args.speaker}")

    workdir = os.path.join(
        os.path.dirname(os.path.abspath(args.output)), ".audiobook_tmp"
    )
    os.makedirs(workdir, exist_ok=True)
    files = []
    try:
        for idx, seg in enumerate(segments, 1):
            audio = synth_with_retry(
                headers, seg, args.speaker, args.speed, args.loudness, args.sample_rate
            )
            p = os.path.join(workdir, f"seg_{idx:05d}.mp3")
            with open(p, "wb") as f:
                f.write(audio)
            files.append(p)
            print(f"  [{idx}/{len(segments)}] {len(seg)}字 -> {p}")
        print("拼接中 ...")
        if ffmpeg_available() and not args.keep:
            concat_with_ffmpeg(files, args.output, args.gap)
        else:
            concat_binary(files, args.output)
        print(f"完成: {os.path.abspath(args.output)}")
    finally:
        if not args.keep:
            import shutil
            shutil.rmtree(workdir, ignore_errors=True)


if __name__ == "__main__":
    main()
