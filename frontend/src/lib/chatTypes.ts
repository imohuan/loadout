/**
 * 对话预览用类型定义。
 *
 * 来源：backup/codex-base-ui/web/src/types/message.ts 的 NormalizedMessage / ToolCall /
 * PlanStep / CommandAction，只保留本项目对话预览需要的子集。
 * 注意：与 frontend/src/lib/types.ts（后端 API 模型）完全独立，不要混淆。
 */

export type MessageKind =
  | 'text'
  | 'thinking'
  | 'tool_use'
  | 'error'
  | 'goal_update'
  | 'task_notification'
  | 'interactive_prompt'
  | 'stream'
  | 'item_start'
  | 'turn_complete'
  | 'thread_status';

export interface CommandAction {
  type: 'read' | 'listFiles' | 'search' | 'unknown';
  command: string;
  name?: string;
  path?: string;
  query?: string;
}

export interface ToolCall {
  id: string;
  toolName: string;
  toolInput: any;
  toolResult: { content: string; isError: boolean } | null;
  commandActions?: CommandAction[];
  status: 'running' | 'done';
}

export interface PlanStep {
  step: string;
  status: string;
}

export interface NormalizedMessage {
  kind: MessageKind;
  id?: string;
  sessionId?: string;
  timestamp?: string;
  provider?: string;
  seq?: number;

  role?: 'user' | 'assistant' | 'system';
  content?: string;
  images?: { path: string; data?: string }[];

  itemId?: string;
  itemType?: string;

  toolName?: string;
  toolId?: string;
  toolInput?: unknown;
  toolResult?: { content: string; isError: boolean } | null;
  commandActions?: CommandAction[];
  status?: string;

  turnId?: string;
  isGoal?: boolean;

  goal?: { objective: string; status: string } | null;
}
