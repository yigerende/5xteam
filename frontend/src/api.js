export async function api(path, options = {}) {
  const request = { ...options }
  if (request.body && typeof request.body !== 'string') request.body = JSON.stringify(request.body)
  const response = await fetch(path, {
    ...request,
    headers: { 'Content-Type': 'application/json', ...(request.headers || {}) },
  })
  let payload
  try {
    payload = await response.json()
  } catch {
    throw new Error(`服务响应异常（HTTP ${response.status}）`)
  }
  if (!response.ok || !payload.ok) { const error = new Error(payload.error || `请求失败（HTTP ${response.status}）`); error.status = response.status; throw error }
  return payload.data
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
  const disposition = response.headers.get('Content-Disposition') || ''
  const match = disposition.match(/filename="?([^";]+)"?/i)
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = match?.[1] || fallbackName
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}
