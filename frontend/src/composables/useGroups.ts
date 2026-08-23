import type { NormalizedMessage, CommandAction } from '@/lib/chatTypes'

interface ToolItem {
  id: string;
  toolName: string;
  toolInput: unknown;
  toolResult: { content: string; isError: boolean } | null;
  commandActions?: CommandAction[];
  status: 'running' | 'done';
  subType?: string;
}

interface ToolGroup {
  type: 'tool-group';
  collapsedTitle: string;
  expandedTitle: string;
  firstToolId: string;
  tools: ToolItem[];
}

interface TextBlockGroup {
  type: 'text-block';
  message: NormalizedMessage;
}

interface ThinkingBlockGroup {
  type: 'thinking';
  message: NormalizedMessage;
}

export type Block = ToolGroup | TextBlockGroup | ThinkingBlockGroup;

interface UserGroup {
  type: 'user';
  message: NormalizedMessage;
}

interface AIGroup {
  type: 'ai';
  duration?: string;
  blocks: Block[];
}

type Group = UserGroup | AIGroup;

export function computeGroups(messages: NormalizedMessage[]): Group[] {
  const groups: Group[] = [];
  let currentAIGroup: AIGroup | null = null;
  let bIdx = 0;

  function flushAIGroup() {
    if (currentAIGroup && currentAIGroup.blocks.length > 0) {
      groups.push(currentAIGroup);
    }
    currentAIGroup = null;
  }

  for (const msg of messages) {
    if (msg.role === 'system') continue;

    if (msg.kind === 'text' && msg.role === 'user') {
      flushAIGroup();
      groups.push({ type: 'user', message: msg });
      continue;
    }

    if (!currentAIGroup) {
      currentAIGroup = { type: 'ai', blocks: [] };
    }

    if (msg.kind === 'thinking') {
      const lastBlock = currentAIGroup.blocks[currentAIGroup.blocks.length - 1];

      if (lastBlock && lastBlock.type === 'thinking') {
        lastBlock.message = {
          ...lastBlock.message,
          content: (lastBlock.message.content || '') + '\n' + (msg.content || ''),
        };
      } else {
        currentAIGroup.blocks.push({ type: 'thinking', message: msg });
      }
    } else if (msg.kind === 'tool_use') {
      bIdx++;
      const toolId = msg.id || `tool-${bIdx}-${Date.now()}`;
      const toolItem: ToolItem = {
        id: toolId,
        toolName: msg.toolName || 'unknown',
        toolInput: msg.toolInput,
        toolResult: msg.toolResult ?? null,
        commandActions: msg.commandActions,
        status: (msg.status as 'running' | 'done') || 'done',
        subType: msg.toolName?.toLowerCase().includes('codegraph') ? 'codegraph' : undefined,
      };

      const lastBlock = currentAIGroup.blocks[currentAIGroup.blocks.length - 1];

      if (lastBlock && lastBlock.type === 'tool-group') {
        lastBlock.tools.push(toolItem);
        lastBlock.collapsedTitle = `${lastBlock.tools.length} 个工具调用`;
        lastBlock.expandedTitle = `${lastBlock.tools.length} 个工具调用`;
        lastBlock.firstToolId = lastBlock.firstToolId || toolId;
      } else {
        currentAIGroup.blocks.push({
          type: 'tool-group',
          collapsedTitle: '1 个工具调用',
          expandedTitle: '1 个工具调用',
          firstToolId: toolId,
          tools: [toolItem],
        });
      }
    } else if (msg.kind === 'text' || msg.kind === 'stream') {
      flushAIGroup();
      currentAIGroup = { type: 'ai', blocks: [{ type: 'text-block', message: msg }] };
    }
  }

  flushAIGroup();

  return groups;
}