import { api, request } from '@/lib/api'
import type { ApiKey, Preset, Skill, SkillPlatformStatus } from '@/lib/types'

export function useManagementApi() {
  type PluginResult = {
    plugins: Array<{
      plugin: string
      checks: Array<{ name: string; issues: Array<{ level: string; message: string }> }>
    }>
  }
  const plugins = () => api<PluginResult>('/api/plugins')
  const skills = () => api<Skill[]>('/api/skills')
  const presets = () => api<Preset[]>('/api/presets')
  const skillStatus = () => api<SkillPlatformStatus[]>('/api/skills/status')
  const syncSkills = () => request<{ synced: number }>('/api/skills/sync', 'POST')
  const checkSkillUpdates = () =>
    request<{ updates: string[] }>('/api/skills/check-updates', 'POST')
  const updateStatus = () =>
    request<{ running: boolean }>('/api/skills/update-status', 'GET')
  const restoreBackup = (target: string) => request<void>('/api/skills/restore', 'POST', { target })
  const restoreAllBackups = () => request<{ restored: string[] }>('/api/skills/restore-all', 'POST')
  const installSkill = (body: { name: string; source: string; version: string }) =>
    request<void>('/api/skills/install', 'POST', body)
  const importSkillZip = (file: File, name: string) => {
    const body = new FormData()
    body.set('file', file)
    body.set('name', name)
    return api<void>('/api/skills/import-zip', { method: 'POST', body })
  }
  const deleteSkill = (name: string) =>
    request<void>(`/api/skills/${encodeURIComponent(name)}`, 'DELETE')
  const createPreset = (body: { name: string; skills: string[]; targets: string[] }) =>
    request<void>('/api/presets', 'POST', body)
  const applyPreset = (name: string) => request<void>('/api/presets/apply', 'POST', { name })
  const deletePreset = (name: string) => request<void>('/api/presets', 'DELETE', { name })
  const keys = () => api<{ sk_keys: ApiKey[]; mcp_keys: ApiKey[] }>('/api/keys')
  const createSkKey = (body: { name: string; models: string[] }) =>
    request<{ key: string }>('/api/keys/sk', 'POST', body)
  const deleteSkKey = (id: string) => request<void>(`/api/keys/sk/${id}`, 'DELETE')
  const createMcpKey = (endpoint: string) =>
    request<{ key: string }>('/api/keys/mcp', 'POST', { endpoint })
  const deleteMcpKey = (endpoint: string) => request<void>('/api/keys/mcp', 'DELETE', { endpoint })
  const mcpToolSchema = (serverId: string, toolName: string) =>
    api<{ name: string; description?: string; inputSchema: Record<string, any> }>(
      '/api/mcp-tools/schema?server_id=' +
        encodeURIComponent(serverId) +
        '&tool_name=' +
        encodeURIComponent(toolName),
    )
  const callMcpTool = (body: {
    server_id: string
    tool_name: string
    arguments: Record<string, any>
  }) =>
    request<{ content?: Array<{ type: string; text?: string }>; isError?: boolean }>(
      '/api/mcp-tools/call',
      'POST',
      body,
    )
  const settings = () => api<{ active_preset: string; default_model: string }>('/api/settings')
  const saveSettings = (body: { active_preset: string; default_model: string }) =>
    request<void>('/api/settings', 'PUT', body)
  const changePassword = (body: { old: string; new: string }) =>
    request<void>('/api/change-password', 'POST', body)
  return {
    plugins,
    skills,
    presets,
    skillStatus,
    updateStatus,
    syncSkills,
    checkSkillUpdates,
    restoreBackup,
    restoreAllBackups,
    installSkill,
    importSkillZip,
    deleteSkill,
    createPreset,
    applyPreset,
    deletePreset,
    keys,
    createSkKey,
    deleteSkKey,
    createMcpKey,
    deleteMcpKey,
    mcpToolSchema,
    callMcpTool,
    settings,
    saveSettings,
    changePassword,
  }
}
