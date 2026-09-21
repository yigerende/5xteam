(async () => {
  const fixture = window.gptPayFixture
  if (!fixture) throw new Error('Isolated Pro preview required')
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (ok, message) => { if (!ok) throw new Error(message) }
  const wait = async fn => { for (let i = 0; i < 200; i++) { if (fn()) return; await pause(25) } throw new Error('Delete UI timed out') }
  const button = label => [...document.querySelectorAll('button')].find(b => b.textContent.trim() === label)
  const deleteButton = () => [...document.querySelectorAll('button')].find(b => /批量删除|删除中/.test(b.textContent))
  const refresh = async () => { button('推送设置').click(); await pause(80); button('Pro 账号').click(); await pause(180) }
  fixture.listProfiles = [
    {email:'delete@example.com',management_scope:'pro'},
    {email:'busy@example.com',management_scope:'pro',pro_workflow_running:true},
    {email:'keep@example.com',management_scope:'pro'}
  ]
  await refresh()
  assert(deleteButton().disabled, 'Delete enabled without selection')
  const rows = [...document.querySelectorAll('.pro-table tbody tr')]
  rows[0].querySelector('input[type=checkbox]').click()
  rows[1].querySelector('input[type=checkbox]').click()
  await pause(30)
  assert(!deleteButton().disabled && deleteButton().textContent.includes('2'), 'Selection count absent')
  const originalConfirm = window.confirm
  try {
    let notice = ''
    window.confirm = text => { notice = text; return false }
    deleteButton().click(); await pause(50)
    assert(!fixture.requests.some(r => r.path.endsWith('/delete')), 'Cancelled confirmation deleted accounts')
    assert(notice.includes('不可撤销') && notice.includes('开通订单和银行卡使用记录保留'), 'Deletion scope not explained')
    window.confirm = () => true
    deleteButton().click()
    await pause(50)
    assert(deleteButton().disabled && deleteButton().textContent.includes('删除中'), 'No busy feedback')
    await wait(() => document.body.textContent.includes('批量删除完成：成功 1，失败 1'))
    assert(!document.querySelector('.pro-table').textContent.includes('delete@example.com'), 'Deleted row retained')
    assert(document.querySelector('.pro-table').textContent.includes('keep@example.com'), 'Unselected row removed')
    assert(document.body.textContent.includes('账号正在执行空间合并'), 'Failure detail missing')
    const remaining = [...document.querySelectorAll('.pro-table tbody tr')]
    assert(remaining.find(r => r.textContent.includes('busy@example.com')).querySelector('input').checked, 'Failed row lost selection')
    const calls = fixture.requests.filter(r => r.path.endsWith('/delete'))
    assert(calls.length === 1 && calls[0].body.emails.length === 2, 'Wrong batch or duplicate submission')
    return 'PASS: selected-only batch, confirmation cancellation, busy state, partial failure, retry selection and list refresh'
  } finally { window.confirm = originalConfirm }
})()
