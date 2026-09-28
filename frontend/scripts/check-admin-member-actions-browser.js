// Run on the isolated admin-members-preview.html fixture with agent-browser eval.
(async () => {
  const f = window.membersFixture
  if (!f) throw new Error('Requires isolated members fixture')
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const wait = async fn => { for (let i = 0; i < 150; i++) { if (fn()) return; await pause(30) } throw new Error('UI timeout') }
  const dialog = () => document.querySelector('.members-dialog')
  const row = email => [...dialog().querySelectorAll('tbody tr')].find(el => el.textContent.includes(email))
  const button = (parent, label) => [...parent.querySelectorAll('button')].find(b => b.textContent.trim() === label)
  if (!dialog()) { [...document.querySelectorAll('button')].find(b => b.textContent.trim() === '查看成员').click(); await pause(30); await wait(() => row('child+1@')) }
  assert([...row('child+0@').querySelectorAll('button')].every(b => b.disabled), 'Owner is actionable')
  assert(button(row('child+2@'), '退出').disabled && button(row('child+2@'), '退出').title.includes('AT'), 'Missing AT must disable leave')
  button(row('child+2@'), '踢出').click(); await pause(30)
  assert(dialog().querySelector('.member-confirm').textContent.includes('child+2@example.com'), 'Confirmation target missing')
  button(dialog().querySelector('.member-confirm'), '取消').click(); await pause(30)
  assert(f.removals.length === 0, 'Cancel sent a mutation')
  for (const [email, action, method] of [['child+2@', '踢出', 'mother_kick'], ['child+1@', '退出', 'child_leave']]) {
    button(row(email), action).click(); await pause(30)
    button(dialog().querySelector('.member-confirm'), `确认${action}`).click(); await pause(50)
    assert(dialog().querySelector('.member-progress .spin'), 'Missing spinner')
    assert(dialog().querySelector('.member-progress').textContent.includes('等待母号'), 'Missing backend progress')
    assert([...dialog().querySelectorAll('.member-actions button')].every(b => b.disabled), 'Duplicate member actions allowed')
    assert(dialog().querySelector('[aria-label="关闭成员窗口"]').disabled, 'Can close before result')
    await wait(() => !dialog().querySelector('.member-progress .spin') && !dialog().querySelector('.members-loading'))
    assert(!row(email), 'Members not refreshed after removal')
    assert(dialog().querySelector('.member-progress').textContent.includes(`${action}完成`), 'Success feedback missing')
    assert(f.removals.at(-1).method === method && f.removals.at(-1).team_account_id === 'team-0', 'Wrong method or Team')
  }
  f.failRemoval = true
  button(row('child+4@'), '踢出').click(); await pause(30)
  button(dialog().querySelector('.member-confirm'), '确认踢出').click(); await pause(30)
  await wait(() => !dialog().querySelector('.member-progress .spin'))
  assert(dialog().querySelector('.member-progress').textContent.includes('429') && row('child+4@'), 'Failure must show reason and retain member')
  assert(!button(row('child+4@'), '踢出').disabled, 'Retry unavailable')
  assert(f.removals.length === 3, 'Duplicate mutations')
  return 'Passed: owner protection, missing AT, cancel, explicit kick/leave, streamed progress/spinner, duplicate guard, list refresh and failure retry'
})()
