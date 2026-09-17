// Isolated fixture only: mail-management-preview.html?pro.
(async () => {
  const f = window.mailExportFixture
  if (!f?.items) throw new Error('Requires Pro preview')
  const assert = (ok, text) => { if (!ok) throw new Error(text) }
  const wait = async (fn, message) => { for (let i = 0; i < 400; i++) { if (fn()) return; await new Promise(r => setTimeout(r, 25)) }; throw new Error(message) }
  const email = f.items[0].email
  const row = () => [...document.querySelectorAll('tbody tr')].find(el => el.textContent.includes(email))
  const select = () => row().querySelector('[data-stage="transfer"] select')
  const merge = () => row().querySelector('[title="执行或续跑空间合并"]')
  const setStatus = status => { select().value = status; select().dispatchEvent(new Event('change', { bubbles: true })) }
  Object.assign(f.items[0], { pro_invite_status: 'completed', pro_accept_status: 'completed', pro_transfer_status: 'failed', pro_remove_status: 'pending', pro_last_error: '模拟合并失败', refresh_token_present: false })
  await wait(() => select()?.value === 'failed', 'Failed state not shown')
  assert(merge().disabled, 'Unfinished child step without RT should be blocked')
  const originalConfirm = window.confirm
  let accepted = false
  const confirmations = []
  window.confirm = text => { confirmations.push(text); return accepted }
  try {
    setStatus('completed')
    assert(!f.requests.some(r => r.path.endsWith('/stage')), 'Cancel changed state')
    assert(select().value === 'failed', 'Cancel did not reset select')
    accepted = true
    setStatus('completed')
    await wait(() => select()?.value === 'completed' && !merge().disabled, 'Cannot resume mother cleanup after manual correction')
    assert(confirmations.at(-1).includes('仅修改本地状态'), 'Missing manual correction warning')
    assert(f.mergeCalls.length === 0, 'Editor executed workflow')
    f.mergeStepDelay = 1800
    merge().click()
    await wait(() => row()?.querySelector('[data-stage="remove"]').classList.contains('completed'), 'Resume did not complete')
    assert(f.mergeCalls.join(',') === 'remove', 'Resume repeated completed steps')
    assert(row().textContent.includes('已成功'), 'Merged flag missing')
    assert(merge().disabled, 'Completed workflow can repeat')

    row().querySelector('[title="查看账号全流程日志"]').click()
    await wait(() => document.querySelectorAll('.log-event').length >= 50, 'Logs missing')
    assert(document.querySelector('.pro-log-dialog').innerText.includes('手动修正'), 'Manual edit missing from timeline')
    const failedEvent = [...document.querySelectorAll('.log-event')].find(el => el.innerText.includes('HTTP 429'))
    assert(failedEvent && failedEvent.innerText.includes('全局代理') && failedEvent.innerText.includes('第 2 次'), 'Missing status/attempt/proxy')
    failedEvent.querySelector('summary').click()
    await wait(() => failedEvent.querySelector('pre'), 'Request details did not expand')
    assert(failedEvent.innerText.includes('target_account_id') && failedEvent.innerText.includes('rate_limited'), 'Request/response body missing')
    const dialog = document.querySelector('.pro-log-dialog')
    dialog.scrollTop = dialog.scrollHeight
    await wait(() => document.querySelectorAll('.log-event').length === 64, 'Infinite log loading truncated history')
    const ids = [...document.querySelectorAll('.log-event time')].map(el => el.textContent)
    assert(ids.length === 64, 'Missing loaded events')
    const originalClick = HTMLAnchorElement.prototype.click
    const downloads = []
    HTMLAnchorElement.prototype.click = function () { downloads.push({ name: this.download, href: this.href }) }
    try {
      [...dialog.querySelectorAll('button')].find(el => el.textContent.includes('导出账号日志')).click()
      await wait(() => downloads.length === 1, 'Export failed')
      assert(downloads[0].name === 'pro-fixture-logs.json', 'Wrong export name')
    } finally { HTMLAnchorElement.prototype.click = originalClick }
    assert(f.requests.some(r => r.path.endsWith('/events/export')), 'Wrong export endpoint')
    dialog.querySelector('[title="关闭日志"]').click()
    await wait(() => !document.querySelector('.pro-log-dialog'), 'Logs did not close')
    f.logFailure = true
    row().querySelector('[title="查看账号全流程日志"]').click()
    await wait(() => document.body.innerText.includes('模拟日志加载失败'), 'Log error not shown')
    f.logFailure = false
    const retry = [...document.querySelectorAll('.pro-log-dialog button')].find(el => el.textContent === '重试')
    retry.click()
    await wait(() => document.querySelectorAll('.log-event').length >= 50, 'Log retry failed')
    document.querySelector('.pro-log-dialog [title="关闭日志"]').click()
    return 'Passed: manual confirm/cancel, persisted stage, RT-less mother-only resume, no repeated merge, complete timeline, diagnostics, export, retry'
  } finally { window.confirm = originalConfirm }
})()
