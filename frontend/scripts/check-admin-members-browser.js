// Run with agent-browser eval -b on scripts/admin-members-preview.html.
(async () => {
  const fixture = window.membersFixture
  if (!fixture) throw new Error('Requires isolated members fixture')
  const assert = (ok, message) => { if (!ok) throw new Error(message) }
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const wait = async fn => { for (let i = 0; i < 100; i++) { if (fn()) return; await pause(30) } throw new Error('UI timeout') }
  const dialog = () => document.querySelector('.members-dialog')
  const button = text => [...dialog().querySelectorAll('button')].find(b => b.textContent.trim() === text)
  const requests = () => fixture.requests.filter(r => r.path.endsWith('/members'))
  const open = index => [...document.querySelectorAll('button')].filter(b => b.textContent.trim() === '查看成员')[index].click()
  const close = () => dialog().querySelector('[aria-label="关闭成员窗口"]').click()
  const ready = () => !dialog().querySelector('.members-loading')
  const search = async value => {
    const input = dialog().querySelector('input')
    input.value = value
    input.dispatchEvent(new Event('input', { bubbles: true }))
    dialog().querySelector('form').requestSubmit()
    await pause(20)
  }
  if (dialog()) { close(); await pause(30) }
  open(0)
  await pause(30)
  assert(dialog().textContent.includes('正在查询'), 'Missing loading status')
  assert(button('搜索').disabled && button('刷新').disabled, 'Duplicate requests not disabled')
  await wait(ready)
  assert(dialog().querySelectorAll('tbody tr').length === 25, 'First page must contain 25 members')
  assert(dialog().textContent.includes('共 27 位成员') && dialog().textContent.includes('高级席位 5x') && dialog().textContent.includes('所有者') && dialog().textContent.includes('已停用'), 'Missing member fields')
  button('下一页').click(); await pause(20); await wait(ready)
  assert(dialog().textContent.includes('child+26@example.com') && button('下一页').disabled, 'Wrong second page')
  assert(requests().at(-1).query.offset === '25', 'Incorrect page request')
  await search('child+26@example.com'); await wait(ready)
  assert(requests().at(-1).query.offset === '0' && requests().at(-1).query.query === 'child+26@example.com', 'Search lost plus or did not reset page')
  assert(dialog().querySelectorAll('tbody tr').length === 1 && dialog().textContent.includes('共 1 位成员'), 'Search did not filter')
  const beforeRefresh = requests().length
  button('刷新').click(); await pause(20); await wait(ready)
  assert(requests().length === beforeRefresh + 1, 'Refresh did not query live')
  await search('absent'); await wait(ready)
  assert(dialog().textContent.includes('未找到匹配的成员'), 'Missing empty state')
  await search('error'); await wait(ready)
  assert(dialog().textContent.includes('查询失败') && dialog().textContent.includes('403') && !dialog().querySelector('tbody'), 'Error retained stale members')
  const beforeRetry = requests().length
  button('重试').click(); await pause(20); await wait(ready)
  assert(requests().length === beforeRetry + 1, 'Retry not sent')
  await search('slow')
  close(); await pause(30); open(1); await pause(30); await wait(ready)
  await pause(1000)
  assert(dialog().textContent.includes('测试母号 2') && dialog().textContent.includes('共 0 位成员') && !dialog().textContent.includes('child+'), 'Late response contaminated other mother')
  assert(fixture.aborted > 0, 'Close did not cancel query')
  assert(requests().every(r => r.method === 'GET' && r.query.limit === '25'), 'Unexpected mutation or unbounded query')
  close(); await pause(30); open(0); await pause(30); await wait(ready)
  return 'Passed: loading, dedicated read action, fields, pagination, email search, refresh, empty, 403/retry, disabled mother, cancellation and stale response isolation'
})()
