import { saveDownload } from './api.js'

export async function readMailExport(response, onProgress) {
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}))
    throw new Error(payload.error || `导出请求失败（HTTP ${response.status}）`)
  }
  if (!response.body || !response.headers.get('Content-Type')?.startsWith('application/x-mail-export')) {
    throw new Error('导出响应格式异常，请刷新页面后重试')
  }
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let pending = new Uint8Array(0)
  let metadata = null
  let received = 0
  const parts = []
  const appendFile = (chunk) => {
    if (!chunk.length) return
    received += chunk.length
    if (received > metadata.size) throw new Error('导出文件大小异常')
    parts.push(chunk)
    onProgress({ stage: 'downloading', received, size: metadata.size })
  }
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      if (metadata) {
        appendFile(value)
        continue
      }
      const joined = new Uint8Array(pending.length + value.length)
      joined.set(pending)
      joined.set(value, pending.length)
      pending = joined
      let newline
      while ((newline = pending.indexOf(10)) >= 0) {
        const event = JSON.parse(decoder.decode(pending.subarray(0, newline)))
        pending = pending.subarray(newline + 1)
        if (event.type === 'error') throw new Error(event.error || '导出失败')
        if (event.type === 'progress') {
          onProgress(event)
        } else if (event.type === 'file') {
          if (!Number.isSafeInteger(event.size) || event.size < 0) throw new Error('导出文件大小异常')
          metadata = event
          appendFile(pending)
          pending = new Uint8Array(0)
          break
        } else {
          throw new Error('导出进度响应异常')
        }
      }
    }
    if (!metadata || received !== metadata.size) throw new Error('导出连接中断，文件未下载完整，请重试')
    const headers = new Headers(metadata.headers)
    return { blob: new Blob(parts, { type: headers.get('Content-Type') || 'application/octet-stream' }), headers }
  } finally {
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}

export async function downloadMailExport(body, onProgress, signal) {
  const response = await fetch('/api/mail/accounts/credentials/export-progress', {
    method: 'POST',
    credentials: 'same-origin',
    cache: 'no-store',
    signal,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const { blob, headers } = await readMailExport(response, onProgress)
  return saveDownload(blob, headers, 'mail-accounts')
}
