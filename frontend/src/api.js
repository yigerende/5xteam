// Only reads may recover from a dropped transport. Writes are never replayed:
// a lost response does not tell us whether the server accepted the operation.
export async function api(path, options = {}) {
  const request = { ...options }
  const readOnly = String(request.method || 'GET').toUpperCase() === 'GET'
  const timeoutMs = request.timeoutMs ?? (readOnly ? 30000 : 0)
  delete request.timeoutMs
  const parentSignal = request.signal
  const controller = timeoutMs > 0 ? new AbortController() : null
  let timedOut = false, timer
  const abort = () => controller?.abort()
  if (controller) {
    request.signal = controller.signal
    if (parentSignal?.aborted) abort()
    else parentSignal?.addEventListener('abort', abort, { once: true })
    timer = setTimeout(() => { timedOut = true; controller.abort() }, timeoutMs)
  }
  try {
    if (request.body && typeof request.body !== 'string') request.body = JSON.stringify(request.body)
    for (let attempt = 0; ; attempt++) {
      try {
        const response = await fetch(path, {
          ...request,
          headers: { 'Content-Type': 'application/json', ...(request.headers || {}) },
        })
        let payload
        try { payload = await response.json() }
        catch (error) {
          if (request.signal?.aborted) throw error
          if (error instanceof TypeError) throw error // connection lost mid-body
          const invalid = new Error(`服务响应异常（HTTP ${response.status}）`)
          invalid.status = response.status
          throw invalid
        }
        if (!response.ok || !payload.ok) { const error = new Error(payload.error || `请求失败（HTTP ${response.status}）`); error.status = response.status; throw error }
        return payload.data
      } catch (error) {
        if (!(error instanceof TypeError) || request.signal?.aborted) throw error
        if (!readOnly || attempt >= 1) {
          const failure = new Error(readOnly ? '网络连接中断，未能读取数据，请重试；已显示的数据会保留' : '网络连接中断，未收到操作结果；请先查看当前状态，避免重复提交')
          failure.network = true
          throw failure
        }
        // One short retry shares the original deadline and navigation signal.
        await new Promise((resolve, reject) => {
          const cancelled = () => { clearTimeout(delay); request.signal?.removeEventListener('abort', cancelled); reject(new DOMException('请求已取消', 'AbortError')) }
          const delay = setTimeout(() => { request.signal?.removeEventListener('abort', cancelled); resolve() }, 300)
          if (request.signal?.aborted) cancelled()
          else request.signal?.addEventListener('abort', cancelled, { once: true })
        })
      }
    }
  } catch (error) {
    if (timedOut) { const failure = new Error(readOnly ? '读取数据超时，请重试；已显示的数据会保留' : '等待操作结果超时，请先查看当前状态，避免重复提交'); failure.timeout = true; throw failure }
    throw error
  } finally {
    clearTimeout(timer)
    parentSignal?.removeEventListener('abort', abort)
  }
}

export async function downloadFile(path, fallbackName = 'download.json', options = {}) {
  const request = { ...options }
  if (request.body && typeof request.body !== 'string') request.body = JSON.stringify(request.body)
  const response = await fetch(path, {
    ...request,
    credentials: 'same-origin',
    cache: 'no-store',
    headers: request.body
      ? { 'Content-Type': 'application/json', ...(request.headers || {}) }
      : request.headers,
  })
  if (!response.ok) {
    let message = `请求失败 (${response.status})`
    try {
      const payload = await response.json()
      message = payload?.error || message
    } catch {}
    throw new Error(message)
  }
  const blob = await response.blob()
  return saveDownload(blob, response.headers, fallbackName)
}

export function saveDownload(blob, headers, fallbackName) {
  const disposition = headers.get('Content-Disposition') || ''
  const match = disposition.match(/filename="?([^";]+)"?/i)
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = match?.[1] || fallbackName
  document.body.appendChild(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  return { filename: link.download, headers }
}
