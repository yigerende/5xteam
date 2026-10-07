(async () => {
  const fixture = window.proPushFixture
  if (!fixture) throw new Error('Requires pro-push-preview.html')
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const wait = async (fn, message) => { for (let i = 0; i < 240; i++) { if (fn()) return; await pause(25) } throw new Error(message) }
  const button = text => [...document.querySelectorAll('button')].find(el => el.textContent.trim() === text)
  const panel = provider => [...document.querySelectorAll('.provider-panel')].find(el => el.querySelector(`input[value="${provider}"]`))
  const labels = provider => [...(panel(provider)?.querySelectorAll('.group-box label') || [])]
  const group = (provider, name) => labels(provider).find(el => el.textContent.trim() === name)?.querySelector('input')
  const models = () => document.querySelector('.models-field textarea')
  const editModels = value => { models().value = value; models().dispatchEvent(new Event('input', { bubbles: true })) }
  const groupsCalls = () => fixture.requests.filter(r => r.path.endsWith('/groups'))
  const remount = async () => {
    document.querySelector('[data-testid="switch-page"]').click(); await pause(30)
    document.querySelector('[data-testid="switch-page"]').click(); await pause(80)
    button('推送设置').click(); await pause(30)
  }
  const save = async () => {
    await pause(25)
    await wait(() => button('保存推送设置') && !button('保存推送设置').disabled, 'Save disabled')
    const writes = fixture.requests.filter(r => r.method === 'PUT').length
    button('保存推送设置').click()
    await wait(() => fixture.requests.filter(r => r.method === 'PUT').length > writes && document.body.textContent.includes('Pro 配置已保存'), 'Save did not finish')
    await pause(50)
  }
  await wait(() => button('推送设置'), 'View did not mount')
  assert(groupsCalls().length === 0, 'Account tab should not query downstream groups')
  fixture.delay = 300
  button('推送设置').click(); await pause(40)
  assert(group('sub2', '不降智')?.checked && group('sub2', '备用组')?.checked, 'Saved groups not visible while loading')
  assert(models().value === 'gpt-6-astra', 'Saved models not loaded')
  await wait(() => group('sub2', '其他组'), 'Active groups not loaded')
  assert(groupsCalls().length === 1 && groupsCalls()[0].path.includes('/sub2/'), 'Inactive CPA must not be queried')
  editModels(' gpt-6-astra, gpt-5.2-codex\ngpt-6-astra\n ')
  group('sub2', '不降智').click()
  await pause(25)
  group('sub2', '其他组').click()
  await save()
  assert(JSON.stringify(fixture.saved.sub2.models) === JSON.stringify(['gpt-6-astra', 'gpt-5.2-codex']), 'Models not normalized and saved')
  assert(JSON.stringify(fixture.saved.sub2.group_names) === JSON.stringify(['备用组', '其他组']), 'Group ID/name association changed')
  await remount()
  assert(group('sub2', '备用组')?.checked && group('sub2', '其他组')?.checked, 'Remount lost saved groups')
  assert(models().value === 'gpt-6-astra\ngpt-5.2-codex', 'Remount lost models')
  await wait(() => !button('保存推送设置').disabled, 'Group refresh unfinished')
  button('Pro 账号').click(); await pause(40)
  fixture.fail = 'sub2'
  button('推送设置').click()
  await wait(() => panel('sub2')?.textContent.includes('模拟连接失败'), 'Group loading failure not shown')
  assert(group('sub2', '备用组')?.checked && group('sub2', '其他组')?.checked, 'Failed refresh discarded selected groups')
  fixture.fail = ''; fixture.groups.sub2 = [{ id: 8, name: '备用组（改名）' }]
  panel('sub2').querySelector('.group-box button').click()
  await wait(() => group('sub2', '备用组（改名）'), 'Manual refresh failed')
  assert(group('sub2', '其他组')?.checked, 'Missing remote group silently deselected')
  editModels(' ,\n '); await save()
  assert(fixture.saved.sub2.models.length === 0, 'Blank models must mean unrestricted')
  panel('cpa').querySelector('input[type="radio"]').click()
  await wait(() => groupsCalls().some(r => r.path.includes('/cpa/')), 'Active CPA groups not loaded')
  await wait(() => !button('保存推送设置').disabled, 'CPA request unfinished')
  assert(group('cpa', 'CPA 已保存')?.checked, 'Saved CPA selection lost')
  fixture.delay = 800; fixture.groups.sub2 = [{ id: 99, name: '过期分组' }]
  panel('sub2').querySelector('input[type="radio"]').click(); await pause(40)
  panel('cpa').querySelector('input[type="radio"]').click(); await pause(850)
  assert(!group('sub2', '过期分组'), 'Aborted stale request changed groups')
  assert(!button('保存推送设置').disabled, 'Switching provider left UI busy')
  fixture.groups.sub2 = [{ id: 8, name: '备用组（改名）' }, { id: 10, name: '其他组' }]; fixture.delay = 50
  panel('sub2').querySelector('input[type="radio"]').click()
  editModels('gpt-6-astra\ngpt-5.2-codex'); await save()
  await remount()
  assert(group('sub2', '备用组（改名）')?.checked && models().value.includes('gpt-6-astra'), 'Final saved state not restored')
  assert(!fixture.requests.some(r => r.path.includes('/push') || r.path.includes('/oauth')), 'Settings tests triggered account operations')
  return 'PASS: models save/clear/reload; groups restore on remount, failure and missing remote entries; active provider only; stale responses ignored'
})()
