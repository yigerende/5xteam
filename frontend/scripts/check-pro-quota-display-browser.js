(async () => {
  const fixture = window.gptPayFixture
  if (!fixture) throw new Error('Isolated Pro preview required')
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (ok, message) => { if (!ok) throw new Error(message) }
  const wait = async fn => { for (let i = 0; i < 200; i++) { if (fn()) return; await pause(25) } throw new Error('Quota UI timed out') }
  const button = label => [...document.querySelectorAll('button')].find(node => node.textContent.trim() === label)
  const quota = () => document.querySelector('.auto-flow-cell [data-stage="quota"]')
  const refresh = async () => { button('推送设置').click(); await pause(80); button('Pro 账号').click(); await pause(180) }
  const steps = { login: 'completed', recharge: 'completed', oauth: 'completed', push: 'completed', quota: 'waiting' }
  fixture.profile.pro_auto = { id: 'quota-display', status: 'waiting_quota', stage: 'quota', steps: { ...steps }, quota_used_threshold: 100, next_check_at: new Date(Date.now() + 120000).toISOString() }
  fixture.profile.pro_stage_progress = { steps: { ...steps }, errors: {} }
  await refresh()
  assert(quota().textContent.includes('等待额度'), 'Waiting threshold shown as completed')
  assert(!document.querySelector('.auto-flow-cell .danger-text'), 'Normal wait displayed as a red error')
  document.querySelector('.auto-flow-open').click()
  await wait(() => document.querySelector('.pro-auto-dialog'))
  await wait(() => document.querySelector('.pro-auto-dialog').textContent.includes('等待额度达到 100%'))
  assert(!document.querySelector('.pro-auto-dialog .danger-text:not(button)'), 'Dialog shows ordinary wait as failure')
  await wait(() => button('关闭') && !button('关闭').disabled)
  button('关闭').click()
  for (const status of ['running', 'failed', 'waiting', 'completed']) {
    fixture.profile.pro_stage_progress = { steps: { ...steps, quota: status }, errors: status === 'failed' ? { quota: '额度查询失败：HTTP 503' } : {} }
    await refresh()
    assert(quota().classList.contains(status), 'Wrong stage style: ' + status)
    if (status === 'running') assert(quota().querySelector('.spin'), 'Quota query spinner missing')
    if (status === 'failed') assert(document.querySelector('.auto-flow-cell .danger-text')?.textContent.includes('HTTP 503'), 'Real query error hidden')
    if (status === 'waiting') assert(!document.querySelector('.auto-flow-cell .danger-text'), 'Recovery leaves stale red error')
  }
  fixture.profile.pro_stage_progress = { steps, errors: {} }
  await refresh()
  assert(!fixture.requests.some(r => r.method !== 'GET'), 'Display test sent a mutation')
  return 'PASS: waiting quota, no false red error, dialog threshold, query spinner, actual failure, recovery and completion; no upstream calls'
})()
