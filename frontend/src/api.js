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
