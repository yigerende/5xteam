// Run with agent-browser eval --stdin on pipeline-cost-preview.html?remove.
(async () => {
  const fixture = window.pipelineCostFixture
  if (!fixture?.removalFixture) throw new Error('Requires isolated removal fixture')
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const waitFor = async (check, message) => {
    for (let i = 0; i < 200; i++) {
      if (check()) return
      await new Promise(resolve => setTimeout(resolve, 20))
    }
    throw new Error(message)
  }
  const rows = () => [...document.querySelectorAll('.free-list tbody tr')]
  const button = text => [...document.querySelectorAll('button')].find(el => el.textContent.includes(text))
  const postCount = () => fixture.requests.filter(r => r.method === 'POST').length
  const openRemove = async i => {
    rows()[i].querySelector('[title="更多操作"]').dispatchEvent(new MouseEvent('mouseenter'))
    await waitFor(() => button('立即移出空间'), 'Missing remove menu item')
    const remove = button('立即移出空间')
    assert(!remove.disabled && remove.title.includes('母号专属代理'), 'Manual kick menu not available/explained')
    remove.click()
  }
  const originalConfirm = window.confirm
  const confirmations = []
  let accept = false
  window.confirm = text => { confirmations.push(text); return accept }
  try {
    await waitFor(() => rows().length === 4, 'Missing fixture accounts')
    assert(rows()[0].innerText.includes('子号自己退出'), 'Fixture does not exercise override')
    await openRemove(0)
    await waitFor(() => document.body.innerText.includes('正在由母号踢出子号'), 'Wrong running label')
    assert(document.body.innerText.includes('移出处理中') && !document.body.innerText.includes('退出处理中'), 'Stage label still says leave')
    await waitFor(() => rows()[0].innerText.includes('实际：母号踢出'), 'Actual method not updated')
    assert(postCount() === 1, 'Single removal made extra requests')

    document.querySelector('[aria-label="选择当前页账号"]').click()
    await waitFor(() => !button('批量移出').disabled, 'Batch selection failed')
    button('批量移出').click()
    assert(postCount() === 1, 'Cancelled batch still sent requests')
    assert(confirmations[0].includes('3 个账号') && confirmations[0].includes('强制采用母号踢出') && confirmations[0].includes('按配置间隔'), 'Confirmation omitted scope/method/interval')

    fixture.failures.add('cost-2')
    accept = true
    button('批量移出').click()
    await waitFor(() => document.body.innerText.includes('批量移出空间完成：成功 2，失败 1，跳过 1'), 'Batch result missing')
    assert(postCount() === 4, 'Completed/unselected accounts incorrectly removed')
    const event = (id, phase) => fixture.removalEvents.find(e => e.id === id && e.phase === phase)
    assert(event('cost-2', 'start').time >= event('cost-1', 'end').time, 'Same mother removals overlapped')
    assert(event('cost-3', 'start').time < event('cost-1', 'end').time, 'Different mothers did not run concurrently')
    assert(rows()[1].innerText.includes('实际：母号踢出') && rows()[3].innerText.includes('实际：母号踢出'), 'Batch did not refresh methods')
    assert(fixture.items[2].remove_method === 'child_leave', 'Failed manual action changed cycle policy')

    fixture.failures.clear()
    await openRemove(2)
    await waitFor(() => rows()[2].innerText.includes('实际：母号踢出'), 'Retry failed')
    assert(postCount() === 5, 'Unexpected retry count')
    assert(fixture.requests.filter(r => r.method === 'POST').every(r => /\/remove$/.test(r.path)), 'Wrong mutation endpoint')
    return 'Passed: single kick, running/completed labels, batch confirm/cancel/skip, same-mother serial, different-mother parallel, failure/retry, no real requests'
  } finally {
    window.confirm = originalConfirm
  }
})()
