# 火山方舟 视频理解 API 文档笔记

来源: https://docs.volcengine.com/docs/82379/1895586
抓取日期: 2026-09-01

## 概述

视觉大模型可理解视频中的视觉信息（描述物体、分析动作逻辑等）。本插件当前只做了图片识别，需追加视频理解。

## API 接口

Responses API 和 Chat API 均支持视频输入。

### 视频传入的 3 种方式

1. **Files API 上传（推荐）**
   - 默认存储空间：最大 512 MB
   - TOS Bucket 存储：最大 2 GB
   - 上传返回 `file_id`，文件默认存储 7 天（1-30 天可配）
   - 文件状态变 `active` 后可用
   - 可复用、减少预处理时延

2. **Base64 编码传入**
   - 视频文件 < 50 MB，请求体 < 64 MB
   - 格式：`data:{mime_type};base64,{base64_data}`

3. **视频 URL 传入**
   - 公网可访问 URL，视频 < 50 MB

## 消息格式

### Chat API（type = video_url）
```json
{
  "model": "doubao-seed-2-1-pro-260628",
  "messages": [{
    "role": "user",
    "content": [
      {"type": "video_url", "video_url": {"file_id": "file-xxx"}},
      {"type": "video_url", "video_url": {"url": "https://.../demo.mp4", "fps": 1}},
      {"type": "text", "text": "描述视频"}
    ]
  }]
}
```

### Responses API（type = input_video）
```json
{
  "model": "doubao-seed-2-1-pro-260628",
  "input": [{
    "role": "user",
    "content": [
      {"type": "input_video", "file_id": "file-xxx"},
      {"type": "input_video", "video_url": "https://.../demo.mp4", "fps": 1},
      {"type": "input_text", "text": "描述视频"}
    ]
  }]
}
```

## 上传 Files API
```bash
curl https://ark.cn-beijing.volces.com/api/v3/files \
  -H "Authorization: Bearer $ARK_API_KEY" \
  -F 'purpose=user_data' \
  -F 'file=@demo.mp4' \
  -F 'preprocess_configs[video][fps]=0.3'
```

TOS 版本支持最大 2GB、`max_video_tokens`、`min_frames`：
```bash
-F "preprocess_configs[video][max_video_tokens]=200000"
-F "preprocess_configs[video][min_frames]=16"
```

## 精细度控制（fps）

- 默认 1（每秒抽一帧）
- 画面变化剧烈/要数动作次数 → 调高 fps（最高 5）
- 画面静态/只数人数 → 调低 fps（最低 0.2）省 token
- 感知时序：模型把时间戳+图像拼接，能回答"事件发生在什么时候"

## 抽帧策略 / token 上限

- 单视频最大 token 80k
- doubao-seed-1.8 及之前：抽帧数 [16, 640]，单帧 max tokens 离散取值默认 640
- doubao-seed-1.8 / 2.0 及后续：抽帧数 [16, 1280]，max_frame_tokens 默认 384
- min_frames 默认 16
- max_video_tokens 默认 81920（80*1024）
- 时间戳格式：2.0 之前 `[4.0 second]`，2.0 之后 `4.0 second`

## 视频格式

- MP4 (.mp4, video/mp4)
- AVI (.avi, video/x-msvideo)
- MOV (.mov, video/quicktime)
- 不支持 TS，格式需小写

## 流式输出

支持 `stream=True`，事件含 ResponseTextDeltaEvent / ResponseCompletedEvent 等。

## 视频理解工作原理

"帧与时间戳的结构化拼接"：每帧图像前插入时间戳文本，形成"时间戳+图像"有序序列。等效于多图请求（time text + image_url 交替）。

## 关键结论（对插件追加）

- 视频识别 = 把视频当作新的多模态内容，通过 Chat API 的 `video_url` 块 / Responses API 的 `input_video` 块传入
- 需要识别消息里视频的传入方式：file_id / base64 / url 三种
- 视频理解同样要走"识别 → 用文本描述替换 → 再交给主模型"的改造管线（与图片插件一致）
- 需要考虑：视频识别耗时较长（抽帧+理解），可能需流式/异步处理；文件大（最多 2GB）不能塞进内存，要落盘或走 file_id
