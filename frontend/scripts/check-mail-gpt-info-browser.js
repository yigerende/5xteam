// Run through agent-browser eval on the isolated mail-management fixture.
(async () => {
  const fixture = window.mailExportFixture
  if (!fixture) throw new Error('Requires isolated mail fixture')
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const button = name => [...document.querySelectorAll('button')].find(el => el.textContent.trim() === name)
  const click = async name => { const el = button(name); assert(el && !el.disabled, name); el.click(); await pause(40) }
  const wait = async predicate => { for (let i = 0; i < 250; i++) { if (predicate()) return; await pause(40) } throw new Error('GPT info UI timeout') }
  const close = async () => { document.querySelector('[aria-label="关闭 GPT 信息进度"]').click(); await pause(40) }
  const posts = () => fixture.requests.filter(r => r.path === '/api/mail/accounts/refresh-info')
  assert(posts().length === 0, 'List load must not start remote requests')
  assert(button('批量刷新 GPT 信息').disabled, 'Empty selection must be disabled')
  document.querySelector('thead input').click()
  await pause(40)
  document.querySelector('.pagination button:last-child').click()
  await wait(() => document.querySelector('tbody input')?.getAttribute('aria-label').includes('fixture-11'))
  document.querySelector('tbody input').click()
  await pause(40)
  fixture.infoDelay = 220
  await click('批量刷新 GPT 信息')
  await wait(() => document.querySelector('.gpt-info-dialog progress'))
  assert(posts().length === 1 && posts()[0].body.emails.length === 11, 'Cross-page selected-only request')
  await close()
  await click('GPT 信息进度')
  assert(posts().length === 1, 'Opening progress must not duplicate job')
  await wait(() => document.querySelector('.gpt-info-counters')?.textContent.includes('已完成'))
  assert(document.querySelector('.gpt-info-counters').textContent.includes('11 / 11'), 'Completion count')
  assert(document.querySelectorAll('.gpt-info-table tbody tr').length === 10, 'Progress must be paginated')
  assert(document.querySelector('.gpt-info-table').textContent.includes('HTTP 401'), 'Failures must be visible')
  assert(document.querySelector('.gpt-info-table').textContent.includes('2023-11-15 06:13:20'), 'Beijing creation time')
  document.querySelector('.gpt-info-dialog .pagination button:last-child').click()
  await wait(() => document.querySelectorAll('.gpt-info-table tbody tr').length === 1)
  await close()
  assert(document.querySelector('.gpt-info-fields').textContent.includes('Pro'), 'Persisted plan displayed')
  document.querySelector('.action-menu>button').dispatchEvent(new MouseEvent('mouseenter'))
  await pause(60)
  fixture.infoDelay = 10000
  await click('刷新 GPT 信息')
  await wait(() => posts().length === 2)
  assert(posts()[1].body.emails.length === 1 && posts()[1].body.emails[0] === 'fixture-11@example.com', 'Single action must target correct account')
  fixture.infoMissing = true
  await wait(() => document.querySelector('.gpt-info-error')?.textContent.includes('服务已重启'))
  await close()
  assert(!button('批量刷新 GPT 信息').disabled, 'Expired job must release controls')
  fixture.infoMissing = false
  return 'Passed: manual-only, selection, cross-page batch, nonblocking progress, no duplicate start, partial/401, Beijing date, paging, single action, restart recovery'
})()
