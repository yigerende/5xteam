import { api } from './api.js'

export function mailTaskTerminal(job) {
  return ['success', 'failed', 'cancelled', 'challenge'].includes(job.status) ||
    ['success', 'failed', 'cancelled', 'challenge'].includes(job.state)
}

function pause(ms, signal) {
  return new Promise((resolve, reject) => {
    const abort = () => { clearTimeout(timer); reject(new DOMException('已停止读取任务进度', 'AbortError')) }
    const timer = setTimeout(() => { signal?.removeEventListener('abort', abort); resolve() }, ms)
    if (signal?.aborted) abort()
    else signal?.addEventListener('abort', abort, { once: true })
  })
}

// Mail and Pro share exactly the same task endpoints and polling protocol.
// Never retry the POST: a lost response must not create a duplicate login.
export async function runMailAccountTask(account, mode, onUpdate = () => {}, { signal, request = api, wait = pause } = {}) {
  const kind = mode === 'oauth' ? 'oauth' : 'login'
  const started = await request(`/api/mail/accounts/${encodeURIComponent(account.email)}/${kind}`, { method: 'POST', body: {}, signal })
  let job = started.job || started
  if (!job.job_id) throw new Error('任务没有返回任务 ID')
  onUpdate(job)
  const path = `/api/mail/${kind}/${encodeURIComponent(job.job_id)}`
  while (!mailTaskTerminal(job)) {
    await wait(1800, signal)
    const result = await request(path, { signal, cache: 'no-store' })
    job = result.job || result
    onUpdate(job)
  }
  if (job.status !== 'success') throw new Error(job.error || job.error_hint || '任务执行失败')
  return job
}
