(async () => {
  const fixture = window.proPushFixture
  if (!fixture) throw new Error('Requires pro-push-preview.html')
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const wait = async (fn, message) => { for (let i = 0; i < 240; i++) { if (fn()) return; await pause(25) } throw new Error(message) }
  const button = text => [...document.querySelectorAll('button')].find(el => el.textContent.trim() === text)
  const group = name => [...document.querySelectorAll('.post-merge-group-box label')].find(el => el.textContent.trim() === name)?.querySelector('input')
  const remount = async () => {
    document.querySelector('[data-testid="switch-page"]').click(); await pause(30)
    document.querySelector('[data-testid="switch-page"]').click(); await pause(100)
  }
  const save = async () => {
    await pause(30)
    await wait(() => button('保存推送设置') && !button('保存推送设置').disabled, 'Save disabled')
    const count = fixture.requests.filter(r => r.method === 'PUT').length
    button('保存推送设置').click()
    await wait(() => fixture.requests.filter(r => r.method === 'PUT').length > count && !button('保存推送设置').disabled, 'Save failed')
    await pause(50)
  }
  const originalGroups = JSON.stringify(fixture.saved.sub2.group_ids)
  button('推送设置').click()
  await wait(() => group('其他组'), 'Post-merge selector missing')
  assert(![...document.querySelectorAll('.post-merge-group-box input')].some(el => el.checked), 'Default must preserve original groups')
  group('不降智').click(); await pause(30); group('其他组').click()
  await save()
  assert(JSON.stringify(fixture.saved.post_merge_group_ids) === '[7,10]', 'Multiple target groups not saved')
  assert(JSON.stringify(fixture.saved.post_merge_group_names) === '["不降智","其他组"]', 'Target group names not saved')
  assert(JSON.stringify(fixture.saved.sub2.group_ids) === originalGroups, 'Target selector modified initial push groups')
  fixture.fail = 'sub2'
  await remount(); button('推送设置').click()
  await wait(() => document.body.textContent.includes('模拟连接失败'), 'Failure not visible')
  assert(group('不降智')?.checked && group('其他组')?.checked, 'Remount or failed refresh lost target selection')
  fixture.fail = ''; fixture.groups.sub2 = [{ id: 7, name: '不降智' }, { id: 8, name: '备用组' }]
  document.querySelector('.post-merge-group-box button').click()
  await wait(() => !button('保存推送设置').disabled, 'Refresh did not finish')
  assert(group('其他组')?.checked, 'Missing remote group silently cleared target selection')
  group('不降智').click(); await pause(30); group('其他组').click(); await save()
  assert(fixture.saved.post_merge_group_ids.length === 0 && fixture.saved.post_merge_group_names.length === 0, 'Clear selection did not persist')
  await remount(); button('推送设置').click(); await pause(100)
  assert(![...document.querySelectorAll('.post-merge-group-box input')].some(el => el.checked), 'Cleared target reappeared')
  fixture.accounts = [{email:'merged@example.com',management_scope:'pro',space_merged_once:true,
    pro_invite_status:'completed',pro_accept_status:'completed',pro_transfer_status:'completed',pro_remove_status:'completed',
    pro_post_merge_groups:{status:'failed',error:'分组服务暂不可用',group_ids:[7,10]}}]
  await remount()
  await wait(() => button('重试调整分组'), 'Retry button missing for removed account')
  assert(document.querySelector('[data-stage="remove"] select').value === 'completed', 'Grouping failure changed removal display')
  assert(document.body.textContent.includes('分组调整失败') && document.body.textContent.includes('分组服务暂不可用'), 'Failure status or reason missing')
  button('重试调整分组').click()
  await wait(() => document.querySelector('.post-merge-group-status .spin'), 'No grouping spinner')
  assert(button('重试调整分组').disabled, 'Repeated clicks permitted')
  await wait(() => document.body.textContent.includes('分组已调整'), 'Completion not shown')
  assert(!button('重试调整分组'), 'Completed adjustment remains retryable')
  assert(fixture.requests.filter(r => r.path.endsWith('/merge')).length === 1, 'Retry sent repeated requests')
  assert(!fixture.requests.some(r => r.path.endsWith('/push') || r.path.includes('/oauth')), 'Grouping UI triggered push/login')
  return 'PASS: optional multi-select save/clear/remount/failed refresh; independent push groups; failed/running/completed display and group-only retry'
})()
