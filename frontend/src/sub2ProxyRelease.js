export function proxyBindingID(account) {
  if (account?.push_provider && account.push_provider !== 'sub2') return ''
  return account?.sub2_proxy_binding_id || ''
}

export function proxyReleaseText(binding, now = Date.now()) {
  if (!binding) return '代理状态读取中…'
  switch (binding.state) {
    case 'permanent': return '代理：一直绑定'
    case 'verifying': return '代理绑定确认中'
    case 'retry': return '代理解绑失败，自动重试中'
    case 'unbound': return '代理已解绑'
    case 'changed': return '代理已变更，原解绑任务结束'
    case 'deleted': return '下游账号已删除，解绑任务结束'
  }
  const due = Date.parse(binding.due_at)
  if (!Number.isFinite(due)) return '代理解绑时间待确认'
  if (due <= now) return '代理解绑中'
  return `代理解绑剩余：${Math.ceil((due - now) / 60000)} 分钟`
}

export function proxyReleasePending(binding) {
  return !binding || ['verifying', 'bound', 'retry'].includes(binding.state)
}
