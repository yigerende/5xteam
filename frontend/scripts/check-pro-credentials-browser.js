// Run on the isolated mail-management-preview.html?pro fixture.
(async () => {
  if (!window.mailExportFixture) throw new Error('Requires isolated fixture')
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const wait = async predicate => { for (let i = 0; i < 150; i++) { if (predicate()) return; await pause(20) }; throw new Error('Credential UI assertion timed out') }
  const input = label => document.querySelector(`.credential-dialog [aria-label="${label}"]`)
  const button = text => [...document.querySelectorAll('.credential-dialog button')].find(el => el.textContent.trim() === text)
  const fill = async (label, value) => { const el = input(label); assert(el && !el.readOnly && !el.disabled, `${label} must be editable`); el.value = value; el.dispatchEvent(new Event('input', { bubbles: true })); await pause(20) }
  const open = async index => { document.querySelectorAll('[aria-label="查看、复制和导出 AT / RT"]')[index].click(); await wait(() => input('Access Token (AT)')) }
  const close = async () => { input('关闭凭证窗口').click(); await wait(() => !document.querySelector('.credential-dialog')) }
  if (document.querySelector('.credential-dialog')) await close()
  await open(0)
  let copied = ''
  navigator.clipboard.writeText = async value => { copied = value }
  button('复制 AT').click(); await pause(30)
  assert(copied === 'fixture-at', 'Initial AT copy failed')
  await fill('ChatGPT 密码', 'edited-password')
  await fill('OpenAI 2FA 密钥', 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ')
  assert(button('查看验证码').disabled, 'Unsaved secret must not use old TOTP')
  await fill('Refresh Token (RT)', 'edited-rt')
  await fill('完整 ChatGPT Session', JSON.stringify({ accessToken: 'session-edited-at', user: { email: 'fixture-01@example.com' } }))
  assert(input('Access Token (AT)').value === 'session-edited-at', 'Pasted Session did not update AT')
  await fill('Account ID', 'edited-account-id')
  assert(button('导出 CPA JSON').disabled && button('导出 Sub2 JSON').disabled, 'Unsaved changes must block stale export')
  button('保存修改').click()
  await wait(() => document.querySelector('.credential-success'))
  assert(input('ChatGPT 密码').value === 'edited-password' && input('Refresh Token (RT)').value === 'edited-rt', 'Save reload mismatch')
  await close(); await open(0)
  assert(input('Access Token (AT)').value === 'session-edited-at' && input('Account ID').value === 'edited-account-id', 'Reopen lost edits')
  button('复制 AT').click(); await pause(30)
  assert(copied === 'session-edited-at', 'Copy uses stale AT')
  const downloads = []
  const originalCreate = URL.createObjectURL, originalClick = HTMLAnchorElement.prototype.click, originalRevoke = URL.revokeObjectURL
  const blobs = new Map()
  URL.createObjectURL = blob => { const url = 'blob:fixture-' + blobs.size; blobs.set(url, blob); return url }
  URL.revokeObjectURL = () => {}
  HTMLAnchorElement.prototype.click = function () { downloads.push({ filename: this.download, blob: blobs.get(this.href) }) }
  try {
    button('导出 CPA JSON').click(); await wait(() => downloads.length === 1)
    await wait(() => !button('导出 Sub2 JSON').disabled)
    button('导出 Sub2 JSON').click(); await wait(() => downloads.length === 2)
    for (const download of downloads) {
      const json = JSON.parse(await download.blob.text())
      const data = json.accounts?.[0]?.credentials || json
      assert(data.access_token === 'session-edited-at' && data.refresh_token === 'edited-rt', 'Export contains stale token')
    }
  } finally { URL.createObjectURL = originalCreate; URL.revokeObjectURL = originalRevoke; HTMLAnchorElement.prototype.click = originalClick }
  await close(); await open(1)
  assert(input('Refresh Token (RT)').value === '' && button('导出 CPA JSON').disabled, 'Missing RT must block export')
  await fill('完整 ChatGPT Session', '{invalid')
  assert(input('Access Token (AT)').value === 'fixture-at' && document.querySelector('.credential-error'), 'Invalid Session should preserve AT and explain error')
  await fill('完整 ChatGPT Session', '')
  await close()
  return 'Passed: editable credentials, Session auto-fills AT, save/reopen, copy, CPA/Sub2 download contents, missing RT, invalid JSON'
})()
