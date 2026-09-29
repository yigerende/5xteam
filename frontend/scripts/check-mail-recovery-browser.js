(async () => {
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const wait = async predicate => { for (let i = 0; i < 250; i++) { if (predicate()) return; await new Promise(resolve => setTimeout(resolve, 50)) } throw new Error('Mail recovery UI timeout') }
  const buttons = () => [...document.querySelectorAll('button')]
  const filter = label => buttons().find(p => p.textContent.trim().startsWith(label + ' '))
  const rows = () => [...document.querySelectorAll('tbody tr')].filter(p => p.querySelector('input[type=checkbox]'))
  const f = window.mailExportFixture
  assert(f.items[0].seat_recovery_active, 'requires ?recovery fixture')
  await wait(() => filter('恢复'))
  assert(buttons().indexOf(filter('恢复')) === buttons().indexOf(filter('死号')) + 1, 'recovery filter is not after dead')
  assert(filter('恢复').textContent.includes('2'), 'recovery count missing')
  filter('恢复').click()
  await wait(() => rows().length === 2 && rows().every(p => p.textContent.includes('席位恢复中')))
  assert(f.requests.some(p => p.path === '/api/mail/accounts' && p.query.includes('space_state=recovering')), 'not using server-side recovery filter')
  assert(rows().every(p => !p.textContent.includes('fixture-03')), 'dead account mixed into recovery')
  f.items[0].seat_recovery_active = false
  f.items[0].seat_recovery_left_at = new Date().toISOString()
  // The same visible list refresh must remove a child as soon as departure is confirmed.
  await wait(() => rows().length === 1 && rows()[0].textContent.includes('fixture-02') && filter('恢复').textContent.includes('1'))
  filter('已使用过').click()
  await wait(() => rows().length === 2 && rows().some(p => p.textContent.includes('fixture-01')))
  assert(rows().every(p => p.textContent.includes('已使用过')), 'departed mail-only child still appears unused')
  assert(!f.requests.some(p => /seat-recovery|free-accounts|backend-api|auto-rotation/.test(p.path)), 'mail filter performed remote/Team workflow requests')
  return {ok:true, checks:['filter placement/count','server pagination filter','recovery labels','automatic refresh after departure','used classification','no remote requests']}
})()
