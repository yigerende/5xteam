// Run against the isolated gptpay-preview.html. No real account requests.
(async () => {
  const f = window.gptPayFixture
  if (!f) throw new Error('Requires isolated GPTPay preview')
  const assert = (v, m) => { if (!v) throw new Error(m) }
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const wait = async fn => { for (let i = 0; i < 250; i++) { if (fn()) return; await pause(25) } throw new Error('Recovery UI timed out') }
  const button = text => [...document.querySelectorAll('button')].find(b => b.textContent.trim() === text)
  const dialog = () => document.querySelector('.pro-auto-dialog')
  const stage = key => dialog()?.querySelector(`[data-stage="${key}"]`)
  const steps = { login: 'completed', recharge: 'completed', oauth: 'failed' }
  f.profile.pro_auto = { id: 'paid-auto', order_id: 'keep-order', status: 'failed', stage: 'oauth', error: '原会话已失效', steps: { ...steps }, quota_used_threshold: 85 }
  f.profile.pro_stage_progress = { steps: { ...steps, oauth: 'running' }, errors: {} }
  button('推送设置').click(); await pause(70); button('Pro 账号').click()
  await wait(() => document.querySelector('.auto-flow-open [data-stage="oauth"]')?.classList.contains('running'))
  document.querySelector('.auto-flow-open').click()
  await wait(() => stage('oauth')?.classList.contains('running'))
  assert(stage('oauth').querySelector('.spin'), 'Retry spinner missing')
  assert(!dialog().textContent.includes('原会话已失效'), 'Stale failure shown during retry')
  f.profile.pro_auto = { ...f.profile.pro_auto, status: 'awaiting_push', stage: 'push', error: '', steps: { ...steps, oauth: 'completed', push: 'pending' } }
  f.profile.pro_stage_progress = { steps: { ...f.profile.pro_auto.steps }, errors: {} }
  await wait(() => stage('oauth')?.classList.contains('completed'))
  assert(dialog().textContent.includes('推送当前下游'), 'Missing continuation hint')
  assert(button('继续后续流程'), 'Cannot continue from saved credentials')
  assert(!dialog().querySelector('.danger-text:not(button)'), 'Stale error remains after OAuth success')
  await wait(() => button('关闭') && !button('关闭').disabled); button('关闭').click()
  f.profile.pro_auto = { ...f.profile.pro_auto, status: 'waiting_quota', stage: 'quota', steps: { ...f.profile.pro_auto.steps, push: 'completed', quota: 'waiting' }, next_check_at: new Date(Date.now() + 45000).toISOString() }
  f.profile.pro_stage_progress = { steps: { ...f.profile.pro_auto.steps }, errors: {} }
  document.querySelector('.auto-flow-open').click()
  await wait(() => stage('quota')?.classList.contains('waiting'))
  assert(stage('oauth').classList.contains('completed') && stage('push').classList.contains('completed'), 'Dialog checkpoint mismatch')
  assert(dialog().textContent.includes('等待额度达到 85%') && dialog().textContent.includes('下次检测'), 'Restored quota schedule invisible')
  assert(!button('继续后续流程'), 'Already scheduled task still offers duplicate continuation')
  await wait(() => button('关闭') && !button('关闭').disabled); button('关闭').click()
  assert(!f.requests.some(r => r.method !== 'GET'), 'Display test unexpectedly mutated data')
  return 'PASS: manual retry spinner, old failure cleared, OAuth completion, push continuation, quota waiting and next check shown without writes'
})()
