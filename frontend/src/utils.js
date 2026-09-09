export const statusText = {
  queued: '排队中', running: '执行中', completed: '已完成', failed: '失败',
  partial: '部分完成', cancelled: '已停止', cancelling: '停止中', pending: '等待中',
}

export const operationText = { full: '完整流程', enter: '进入空间', transfer: '合并空间', kick: '移出空间' }

export function shortID(value) {
  if (!value) return '-'
  return value.length > 22 ? `${value.slice(0, 12)}...${value.slice(-6)}` : value
}

export function formatTime(value) {
  if (!value) return '-'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    timeZone: 'Asia/Shanghai',
  }).format(new Date(value))
}

export function extractAccessTokens(value, output = [], seen = new Set()) {
  if (!value || typeof value !== 'object') return output
  if (Array.isArray(value)) {
    value.forEach((item) => extractAccessTokens(item, output, seen))
    return output
  }
  for (const key of ['access_token', 'accessToken']) {
    const token = typeof value[key] === 'string' ? value[key].trim() : ''
    if (token && !seen.has(token)) {
      seen.add(token)
      output.push(token)
    }
  }
  Object.values(value).forEach((child) => extractAccessTokens(child, output, seen))
  return output
}

export function findCredential(value, keys, depth = 0) {
  if (!value || typeof value !== 'object' || depth > 6) return ''
  for (const key of keys) {
    if (typeof value[key] === 'string' && value[key].trim()) return value[key].trim()
  }
  for (const child of Object.values(value)) {
    const found = findCredential(child, keys, depth + 1)
    if (found) return found
  }
  return ''
}

export const findAccessToken = (value) => findCredential(value, ['accessToken', 'access_token'])
export const findRefreshToken = (value) => findCredential(value, ['refreshToken', 'refresh_token'])

export function decodeJWTPayload(token) {
  const parts = token.split('.')
  if (parts.length !== 3) throw new Error('没有找到有效的 Access Token')
  const normalized = parts[1].replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized + '='.repeat((4 - normalized.length % 4) % 4)
  const bytes = Uint8Array.from(atob(padded), (char) => char.charCodeAt(0))
  return JSON.parse(new TextDecoder().decode(bytes))
}

export async function extractTokensFromFiles(fileList) {
  const files = Array.from(fileList || [])
    .filter((file) => /\.json$/i.test(file.name || ''))
    .sort((a, b) => (a.webkitRelativePath || a.name).localeCompare(b.webkitRelativePath || b.name, 'zh-CN'))
  const tokens = []
  const seen = new Set()
  let invalid = 0
  for (const file of files) {
    try {
      const parsed = JSON.parse(await file.text())
      const before = tokens.length
      extractAccessTokens(parsed, tokens, seen)
      if (tokens.length === before) invalid++
    } catch {
      invalid++
    }
  }
  return { files, tokens, invalid }
}

export function maskProxyURL(value) {
  return String(value || '').replace(/:\/\/([^/@:]+):([^@]+)@/, '://$1:***@')
}

export function progressLabel(progress) {
  if (!progress) return '无记录'
  const stages = []
  if (progress.entered_at) stages.push('已进入')
  if (progress.transferred_at) stages.push('已合并')
  if (progress.removed_at) stages.push('已移出')
  return stages.join(' · ') || '无已完成阶段'
}
