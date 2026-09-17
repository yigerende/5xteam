// Isolated preview only; no request reaches the real backend.
(async () => {
  const fixture = window.pipelineCostFixture
  if (!fixture?.seatFixture) throw new Error('Requires ?seats fixture')
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const card = label => [...document.querySelectorAll('.metric-card')].find(el => el.querySelector('span')?.textContent === label)
  const value = label => card(label)?.querySelector('strong')?.textContent.trim()
  const settle = () => new Promise(resolve => setTimeout(resolve, 100))
  await settle()
  assert(value('母号 5x 席位快照') === '2', 'Disabled or deleted snapshot counted')
  assert(value('5x 席位总数 / 剩余') === '2 / 0', 'Disabled usage consumed enabled capacity')
  assert(value('7天平均剩余额度') === '0%', 'Disabled quota affected average')
  assert(card('7天平均剩余额度').textContent.includes('已查询 1 个账号'), 'Quota count differs from average scope')
  fixture.rotationAdmins[1].rotation_disabled = false
  await settle()
  assert(value('母号 5x 席位快照') === '12', 'Re-enable did not restore stored seats')
  assert(value('5x 席位总数 / 剩余') === '12 / 8', 'Re-enable usage is wrong')
  fixture.rotationAdmins.forEach(admin => { admin.rotation_disabled = true })
  await settle()
  assert(value('5x 席位总数 / 剩余') === '0 / 0', 'All disabled still shows capacity')
  fixture.rotationAdmins[0].rotation_disabled = false
  await settle()
  assert(value('5x 席位总数 / 剩余') === '2 / 0', 'Toggle result is stale')
  assert(fixture.requests.every(req => req.method === 'GET'), 'Display check caused remote operation')
  assert(!document.querySelector('.message-bar.error'), 'Page reported an error')
  return 'Passed: disabled/deleted snapshots excluded, matching usage and quota, re-enable, all disabled, reactive updates, no remote operations'
})()
