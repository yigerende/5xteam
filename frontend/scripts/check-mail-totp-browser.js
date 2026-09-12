// Run with agent-browser eval --stdin on mail-management-preview.html?totp-no-at.
(async () => {
  if (!window.mailExportFixture) throw new Error('Requires isolated mail fixture')
  const fixture = window.mailExportFixture
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const button = name => [...document.querySelectorAll('.credential-dialog button')].find(el => el.textContent.trim() === name)
  const wait = async predicate => {
    for (let i = 0; i < 100; i++) { if (predicate()) return; await pause(20) }
    throw new Error('TOTP UI assertion timed out')
  }
  const open = async index => {
    document.querySelectorAll('.action-menu>button')[index].dispatchEvent(new MouseEvent('mouseenter'))
    await pause(30)
    const view = [...document.querySelectorAll('.action-menu-popover button')].find(el => el.textContent.includes('查看、复制'))
    assert(view && !view.disabled, 'Credential view must be available without AT')
    view.click()
    await wait(() => document.querySelector('[aria-label="OpenAI 2FA 密钥"]'))
  }
  const close = async () => { document.querySelector('[aria-label="关闭凭证窗口"]').click(); await pause(30) }
  let copied = ''
  navigator.clipboard.writeText = async value => { copied = value }
  await open(0)
  assert(button('查看验证码') && !document.querySelector('.credential-totp-code code'), 'Code must be requested on demand')
  button('查看验证码').click()
  await wait(() => document.querySelector('.credential-totp-code code'))
  assert(document.querySelector('.credential-totp-code code').textContent === '012345', 'Leading zero lost')
  button('复制验证码').click()
  await pause(30)
  assert(copied === '012345', 'Code copy mismatch')
  fixture.totpValidity = 100
  button('刷新').click()
  await wait(() => document.querySelector('.credential-totp-code small')?.textContent === '已过期')
  assert(button('复制验证码').disabled && document.querySelector('.credential-totp-code code').textContent === '------', 'Expired code must not be copyable')
  fixture.totpValidity = 30000
  fixture.totpValue = '654321'
  const originalNow = Date.now
  try {
    Date.now = () => 0
    button('刷新').click()
    await wait(() => document.querySelector('.credential-totp-code code')?.textContent === '654321')
    assert(document.querySelector('.credential-totp-code small').textContent.includes('秒后过期'), 'PC time changed validity')
  } finally {
    Date.now = originalNow
  }
  fixture.totpFail = true
  button('刷新').click()
  await wait(() => document.querySelector('.credential-dialog [role=alert]'))
  assert(!document.querySelector('.credential-totp-code code') && !button('查看验证码').disabled, 'Error retained stale code or disabled retry')
  fixture.totpFail = false
  fixture.totpDelay = 250
  button('查看验证码').click()
  await close()
  await open(1)
  await pause(350)
  assert(document.querySelector('[aria-label="OpenAI 2FA 密钥"]').value === '未配置', 'Wrong account secret')
  assert(!document.querySelector('.credential-totp-code') && !document.querySelector('.credential-dialog [role=alert]'), 'Late TOTP response leaked into another account')
  await close()
  fixture.totpDelay = 0
  return 'Passed: on-demand code, leading zero, copy, expiry, refresh, wrong PC time, failure recovery, account isolation'
})()
