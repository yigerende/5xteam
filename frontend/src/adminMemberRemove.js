export async function removeAdminMember(adminID, input, onProgress) {
  const response = await fetch(`/api/admin-accounts/${encodeURIComponent(adminID)}/members/remove`, {
    method: 'POST', credentials: 'same-origin', cache: 'no-store',
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input),
  })
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}))
    throw new Error(payload.error || `操作失败（HTTP ${response.status}）`)
  }
  if (!response.body || !response.headers.get('Content-Type')?.startsWith('application/x-ndjson')) throw new Error('进度响应异常，请刷新成员列表核实结果')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = '', completed = false
  const accept = line => {
    if (!line.trim()) return
    const event = JSON.parse(line)
    if (event.type === 'error') throw new Error(event.error || '操作失败')
    if (event.type === 'done') completed = true
    onProgress(event)
  }
  try {
    while (true) {
      const { done, value } = await reader.read()
      buffer += done ? decoder.decode() : decoder.decode(value, { stream: true })
      let newline
      while ((newline = buffer.indexOf('\n')) >= 0) { accept(buffer.slice(0, newline)); buffer = buffer.slice(newline + 1) }
      if (done) break
    }
    accept(buffer)
    if (!completed) throw new Error('进度连接中断，请刷新成员列表核实结果后再操作')
  } finally { await reader.cancel().catch(() => {}); reader.releaseLock() }
}
