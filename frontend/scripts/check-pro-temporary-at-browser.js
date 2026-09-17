// Isolated fixture only: never logs in to an actual account.
(async () => {
  if (!window.mailExportFixture) throw new Error('Requires isolated preview fixture')
  const fixture = window.mailExportFixture
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const wait = async fn => { for (let i = 0; i < 400; i++) { if (fn()) return; await pause(25) }; throw new Error('Timed out waiting for temporary AT UI') }
  const row = email => [...document.querySelectorAll('tbody tr')].find(el => el.textContent.includes(email))
  const button = email => row(email).querySelector('[title="获取临时 AT"]')
  const dialog = () => document.querySelector('.temporary-at-dialog')
  const clickText = text => [...dialog().querySelectorAll('button')].find(el => el.textContent === text).click()
  const posts = () => fixture.requests.filter(r => r.path.startsWith('/api/mail/accounts/') && r.path.endsWith('/login'))
  const first = 'fixture-01@example.com', second = 'fixture-02@example.com'
  assert(document.querySelectorAll('[title="获取临时 AT"]').length === 10, 'Single-account buttons missing')
  const batch = [...document.querySelectorAll('button')].find(el => el.textContent.includes('批量获取临时 AT'))
  assert(batch.disabled, 'Empty selection must be disabled')
  assert(!button(second).disabled, 'Temporary AT must not require existing RT')
  assert(row(second).querySelector('[title="刷新 AT"]').disabled, 'Existing RT refresh remains distinct')
  button(first).click()
  await wait(dialog)
  assert(posts().length === 0, 'Must not login before confirmation')
  clickText('取消')
  await wait(() => !dialog())
  assert(posts().length === 0, 'Cancel must not login')
  button(first).click()
  await wait(dialog)
  clickText('确认获取')
  await wait(() => posts().length === 1)
  assert(button(first).disabled && batch.disabled, 'Running requests must disable duplicate actions')
  await wait(() => dialog().textContent.includes('正在登录并验证'))
  await wait(() => dialog().textContent.includes('已完成 1 / 1'))
  assert(dialog().textContent.includes('成功 1') && dialog().textContent.includes('AT 与 Session 已保存'), 'Single success missing')
  clickText('关闭')
  await wait(() => !dialog())
  // Selected-only parallel work and per-account failure isolation.
  row(first).querySelector('input[type="checkbox"]').click()
  row(second).querySelector('input[type="checkbox"]').click()
  await pause(20)
  fixture.loginFailEmails = [second]
  fixture.loginDelay = 2200
  const before = posts().length
  batch.click()
  await wait(dialog)
  assert(dialog().textContent.includes('批量获取临时 AT'), 'Batch title missing')
  clickText('确认获取')
  await wait(() => posts().length === before + 2)
  assert(posts().slice(before).map(r => decodeURIComponent(r.path.split('/')[4])).sort().join(',') === [first, second].sort().join(','), 'Requested unselected accounts')
  await wait(() => dialog().textContent.includes('已完成 2 / 2'))
  assert(dialog().textContent.includes('成功 1') && dialog().textContent.includes('失败 1') && dialog().textContent.includes('模拟验证失败'), 'Partial failure progress missing')
  clickText('关闭')
  await wait(() => !dialog())
  // Creation errors and lost jobs cannot leave the UI spinning forever.
  fixture.loginMissingJob = true
  button(second).click()
  await wait(dialog)
  clickText('确认获取')
  await wait(() => dialog().textContent.includes('已完成 1 / 1'))
  assert(dialog().textContent.includes('任务没有返回任务 ID'), 'Missing job error absent')
  clickText('关闭')
  await wait(() => !dialog())
  fixture.loginMissingJob = false
  fixture.loginStatusMissing = true
  button(second).click()
  await wait(dialog)
  clickText('确认获取')
  await wait(() => dialog().textContent.includes('已完成 1 / 1'))
  assert(dialog().textContent.includes('模拟服务重启'), 'Lost polling job error absent')
  clickText('关闭')
  await wait(() => !dialog())
  fixture.loginStatusMissing = false
  return 'Passed: single/batch buttons, no-RT account, separate RT refresh, confirmation/cancel, selected-only parallel requests, live logs, partial failure, missing job, restart, completion unlock'
})()
