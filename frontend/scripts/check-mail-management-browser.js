// Run through agent-browser eval --stdin on mail-management-preview.html only.
(async () => {
  if (!window.mailExportFixture) throw new Error('Requires isolated mail fixture')
  const pause = () => new Promise(resolve => setTimeout(resolve, 40))
  const wait = async predicate => {
    for (let i = 0; i < 150; i++) { if (predicate()) return; await pause() }
    throw new Error('UI assertion timed out')
  }
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const button = name => [...document.querySelectorAll('button')].find(el => el.textContent.trim() === name || el.textContent.trim().startsWith(`${name}（`))
  const click = async name => { const el = button(name); assert(el && !el.disabled, `Missing/enabled button: ${name}`); el.click(); await pause() }
  const selected = () => Number(document.body.innerText.match(/已选 (\d+)/)?.[1] || 0)
  const field = label => [...document.querySelectorAll('[role=dialog] label')].find(el => el.textContent.trim() === label)?.querySelector('input')
  const toggle = async label => { const el = field(label); assert(el, label); el.click(); await pause() }
  const selectMode = async value => { const el = document.querySelector('[role=dialog] select'); el.value = value; el.dispatchEvent(new Event('change', { bubbles: true })); await pause() }
  const close = async () => { if (document.querySelector('[role=dialog]')) await click('关闭') }
  const fixture = window.mailExportFixture
  fixture.fail = false
  fixture.delay = 20
  await close()
  await click('条件选择')
  await selectMode('valid')
  await toggle('有 RT')
  await toggle('有 ChatGPT 密码')
  await toggle('有 2FA')
  await click('全选')
  assert(selected() === 1, 'Combined conditions must select exactly one fixture')
  await click('条件选择')
  await click('重置')
  await click('本页选择')
  assert(selected() === 10, 'Current page selection')
  document.querySelector('.pagination button:last-child').click()
  await wait(() => document.querySelector('tbody input')?.getAttribute('aria-label').includes('fixture-11'))
  assert(selected() === 10 && document.querySelectorAll('tbody input:checked').length === 0, 'Selection must persist across pages')
  document.querySelector('thead input').click()
  await pause()
  assert(selected() === 20, 'Page checkbox must preserve other pages')
  await click('条件选择')
  await click('全选')
  assert(selected() === 24, 'All pages selection')
  document.querySelector('tbody input').click()
  await pause()
  assert(selected() === 23, 'Deselection after select all')
  await click('导出文本')
  assert(!field('附带 AT').checked && !field('附带 RT').checked, 'Token options default off')
  await toggle('附带 AT')
  await toggle('附带 RT')
  await click('导出')
  await wait(() => document.querySelector('[role=progressbar]')?.getAttribute('aria-valuenow') > 0)
  await wait(() => document.querySelector('[role=dialog]')?.innerText.includes('导出完成'))
  let request = fixture.requests.at(-1).body
  assert(request.emails.length === 23 && request.include_at && request.include_rt && request.format === 'text', 'Text export snapshot/options')
  assert(!request.emails.includes('fixture-11@example.com'), 'Unselected account must not export')
  await close()
  for (const [label, format] of [['CPA', 'cpa'], ['Sub2', 'sub2']]) {
    await click(`批量导出 ${label}`)
    await wait(() => document.querySelector('[role=dialog]')?.innerText.includes('导出完成'))
    assert(fixture.requests.at(-1).body.format === format, 'Incorrect credential export format')
    await close()
  }
  fixture.fail = true
  await click('批量导出 Sub2')
  await wait(() => document.querySelector('[role=dialog]')?.innerText.includes('导出失败'))
  assert(document.querySelector('[role=alert]').innerText.includes('模拟账号已删除'), 'Error must be visible')
  assert(!button('关闭').disabled, 'Failure must restore buttons')
  await close()
  fixture.fail = false
  return 'Passed: combined filters, page/all selection, cross-page persistence, deselection, token options, real progress, all three formats, failure recovery'
})()
