/**
 * aiClassifyWithTools — 流式（stream）路径单元测试
 *
 * 背景：非流式长生成在上游需攒满整段才返回，容易被调用方超时掐断
 * （真实事故：规则作者 30s 超时 → loadout 侧 context canceled）。
 * 改为流式后数据边到边收，配合可配置超时即可稳定完成。
 *
 * 本测试不依赖真实 AI 配置：用进程内 mock HTTP 服务模拟上游，
 * 验证公开接口的可观察行为：
 *   1. 开启流式时请求体带 stream:true，且能跨块拼装 tool_calls 参数
 *   2. 关闭流式时请求体 stream:false，走一次性返回
 *   3. timeoutMs 到期时主动中断（AbortSignal 生效）
 */

import { describe, it, expect } from 'vitest';
import { createServer, type Server } from 'node:http';
import { z } from 'zod';
import { aiClassifyWithTools } from '../src/utils/ai';

const SCHEMA = z.object({
  name: z.string(),
  verdict: z.enum(['unknown', 'quota', 'network']),
});

/** 把整段 SSE 按固定长度切块，逼出「跨块行切割」的解析场景 */
function chunked(sse: string, size = 7): string[] {
  const out: string[] = [];
  for (let i = 0; i < sse.length; i += size) out.push(sse.slice(i, i + size));
  return out;
}

/** 拼一条「tool_calls 分块增量」的 SSE 响应 */
function toolCallSSE(): string {
  return [
    'data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"submit_rule","arguments":""}}]}}]}',
    '',
    'data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\\"name\\":\\"r1\\","}}]}}]}',
    '',
    'data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\\"verdict\\":\\"unknown\\"}"}}]}}]}',
    '',
    'data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}',
    '',
    'data: [DONE]',
    '',
  ].join('\n');
}

/**
 * 启动 mock 上游。
 * @param respond 收到请求后的响应策略；hang 表示不响应（用于验证超时）
 */
async function startMockServer(
  respond: (body: Record<string, unknown>) => { status: number; sse: string } | 'hang',
): Promise<{ port: number; close: () => Promise<void>; bodies: Array<Record<string, unknown>> }> {
  const bodies: Array<Record<string, unknown>> = [];
  let server: Server;
  await new Promise<void>((resolve) => {
    server = createServer((req, res) => {
      let raw = '';
      req.on('data', (d) => (raw += d));
      req.on('end', () => {
        let parsed: Record<string, unknown> = {};
        try {
          parsed = JSON.parse(raw);
        } catch {
          /* 保持空对象，断言时自然暴露 */
        }
        bodies.push(parsed);
        const out = respond(parsed);
        if (out === 'hang') return; // 故意不响应，交给客户端超时
        res.writeHead(out.status, { 'Content-Type': 'text/event-stream' });
        res.end(out.sse);
      });
    });
    server.listen(0, '127.0.0.1', () => resolve());
  });
  const { port } = server.address() as { port: number };
  return {
    port,
    bodies,
    close: () => new Promise<void>((r) => server.close(() => r())),
  };
}

function call(baseUrl: string, extra: Record<string, unknown> = {}) {
  return aiClassifyWithTools('证据：errorCode 4002', SCHEMA, {
    config: { baseUrl, apiKey: 'test-key', model: 'mock' },
    toolName: 'submit_rule',
    maxRetries: 0,
    ...extra,
  });
}

describe('aiClassifyWithTools 流式路径', () => {
  it('开启流式：请求体带 stream:true，且跨块拼装 tool_calls 参数并通过 zod 校验', async () => {
    const { port, close, bodies } = await startMockServer(() => ({
      status: 200,
      sse: chunked(toolCallSSE(), 5).join(''),
    }));
    try {
      const result = await call(`http://127.0.0.1:${port}/v1`, { stream: true });
      expect(bodies[0].stream).toBe(true);
      expect(result).toEqual({ name: 'r1', verdict: 'unknown' });
    } finally {
      await close();
    }
  });

  it('关闭流式：请求体 stream:false，走一次性返回', async () => {
    const sse = [
      'data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"submit_rule","arguments":"{\\"name\\":\\"r2\\",\\"verdict\\":\\"quota\\"}"}}]}}]}',
      '',
      'data: [DONE]',
      '',
    ].join('\n');
    const { port, close, bodies } = await startMockServer(() => ({ status: 200, sse }));
    try {
      const result = await call(`http://127.0.0.1:${port}/v1`, { stream: false });
      expect(bodies[0].stream).toBe(false);
      expect(result).toEqual({ name: 'r2', verdict: 'quota' });
    } finally {
      await close();
    }
  });

  it('超时生效：长时间无响应时按 timeoutMs 中断', async () => {
    const { port, close } = await startMockServer(() => 'hang');
    try {
      const started = Date.now();
      await expect(
        call(`http://127.0.0.1:${port}/v1`, { stream: true, timeoutMs: 800 }),
      ).rejects.toThrow();
      const elapsed = Date.now() - started;
      // 允许一定调度抖动，但必须远小于 vitest 的 150s 兜底
      expect(elapsed).toBeLessThan(10_000);
    } finally {
      await close();
    }
  }, 20_000);
});
