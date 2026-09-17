// Browser fixture only: no real OpenAI request or account modification.
(async () => {
  if (!window.mailExportFixture) throw new Error('Requires isolated preview fixture')
  const fixture = window.mailExportFixture
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const wait = async fn => { for (let i = 0; i < 600; i++) { if (fn()) return; await pause(25) }; throw new Error('OAuth UI timeout') }
  const email = 'fixture-02@example.com'
  const row = () => [...document.querySelectorAll('tbody tr')].find(item => item.textContent.includes(email))
  const button = () => row().querySelector('[title="登录获取 RT / AT"]')
  const dialog = () => document.querySelector('.temporary-at-dialog')
  const click = text => [...dialog().querySelectorAll('button')].find(item => item.textContent === text).click()
  const posts = () => fixture.requests.filter(item => item.path === '/api/mail/accounts/' + encodeURIComponent(email) + '/oauth')
  assert(button() && !button().disabled, 'OAuth login must work without existing RT')
  assert(row().querySelector('[title="重新授权"]'), 'Existing manual OAuth-link action must remain')
  button().click()
  await wait(dialog)
  assert(dialog().textContent.includes('登录获取 RT / AT') && dialog().textContent.includes('Team 轮转第三步'), 'OAuth confirmation is wrong')
  assert(!dialog().textContent.includes('不会获取或替换 RT'), 'Temporary AT text leaked into OAuth confirmation')
  assert(posts().length === 0, 'No login before confirmation')
  click('取消')
  await wait(() => !dialog())
  assert(posts().length === 0, 'Cancelled confirmation made an OAuth request')
  button().click()
  await wait(dialog)
  click('确认获取')
  await wait(() => posts().length === 1)
  assert(button().disabled && row().querySelector('[title="获取临时 AT"]').disabled, 'Login actions must not overlap')
  await wait(() => dialog().textContent.includes('OAuth 登录方式：邮箱验证码'))
  await wait(() => dialog().textContent.includes('继续验证 2FA'))
  await wait(() => dialog().textContent.includes('已完成 1 / 1'))
  assert(dialog().textContent.includes('OAuth 回调换取 RT / AT') && dialog().textContent.includes('RT / AT 已保存'), 'Callback step/success missing')
  assert(!dialog().textContent.includes('fixture-codex-rt'), 'Secret token leaked into progress')
  await wait(() => row().textContent.includes('AT / RT 完整'))
  assert(fixture.requests.some(item => item.path.startsWith('/api/mail/oauth/codex-oauth-')), 'OAuth job status was not polled')
  click('关闭')
  await wait(() => !dialog())
  assert(document.body.textContent.includes('OAuth RT / AT 获取完成：成功 1'), 'Completion banner is wrong')
  fixture.oauthStageDelay = 1
  fixture.oauthFail = true
  button().click()
  await wait(dialog)
  click('确认获取')
  await wait(() => dialog().textContent.includes('已完成 1 / 1'))
  assert(dialog().textContent.includes('模拟 OAuth 验证失败') && dialog().textContent.includes('失败 1'), 'OAuth failure missing')
  click('关闭')
  await wait(() => !dialog())
  fixture.oauthFail = false
  fixture.oauthMissingJob = true
  button().click()
  await wait(dialog)
  click('确认获取')
  await wait(() => dialog().textContent.includes('任务没有返回任务 ID'))
  click('关闭')
  await wait(() => !dialog())
  fixture.oauthMissingJob = false
  fixture.oauthStatusMissing = true
  button().click()
  await wait(dialog)
  click('确认获取')
  await wait(() => dialog().textContent.includes('Codex OAuth 任务不存在'))
  click('关闭')
  await wait(() => !dialog())
  fixture.oauthStatusMissing = false
  row().querySelector('[title="获取临时 AT"]').click()
  await wait(dialog)
  assert(dialog().textContent.includes('不会获取或替换 RT'), 'Temporary AT mode did not reset')
  click('取消')
  await wait(() => !dialog())
  assert(!fixture.requests.some(item => item.path.startsWith('/api/free-accounts') || item.path.endsWith('/push') || item.path.endsWith('/merge') || item.path.endsWith('/oauth/start')), 'Unrelated Team, push, merge or manual-link action triggered')
  assert(!fixture.requests.some(item => item.path.endsWith('/login')), 'OAuth action called temporary AT endpoint')
  return 'Passed: no-RT account, original manual authorization preserved, confirmation/cancel, shared Codex OAuth endpoints, live OTP/2FA/callback steps, token privacy, success, failure, missing job, restart and temporary AT reset'
})()
