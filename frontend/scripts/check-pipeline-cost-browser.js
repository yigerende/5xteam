// Run with agent-browser eval --stdin on pipeline-cost-preview.html.
(async () => {
  if (!window.pipelineCostFixture) throw new Error('Requires isolated cost fixture')
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const cells = () => [...document.querySelectorAll('.cost-cell')]
  assert(cells().length === 4, 'Missing account rows')
  assert(cells()[0].querySelector('strong').textContent === '$25.1788', 'Standard cost changed')
  assert(cells()[0].querySelector('.user-cost').textContent === '用户 $52.0400', 'User cost missing')
  assert(cells()[1].querySelector('.user-cost').textContent === '用户 $0.0000', 'Zero treated as missing')
  assert(cells()[2].querySelector('.user-cost').textContent === '用户 --', 'Missing cost treated as zero')
  assert(!cells()[3].querySelector('.user-cost') && cells()[3].textContent.includes('CPA 不统计'), 'CPA gained dollar costs')
  for (const cell of cells()) {
    const blocks = [...cell.children].map(el => el.getBoundingClientRect())
    assert(blocks.every((rect, i) => !i || rect.top >= blocks[i - 1].bottom), 'Cost lines overlap')
  }
  const quotaButton = document.querySelector('.free-list tbody tr [title="刷新 5小时/7天额度"]')
  assert(quotaButton && !quotaButton.disabled, 'Quota button unavailable')
  quotaButton.click()
  for (let i = 0; i < 100; i++) {
    if (cells()[0].querySelector('.user-cost').textContent === '用户 $60.0000') break
    await new Promise(resolve => setTimeout(resolve, 20))
  }
  assert(cells()[0].querySelector('strong').textContent === '$30.0000', 'Standard cost did not refresh')
  assert(cells()[0].querySelector('.user-cost').textContent === '用户 $60.0000', 'User cost did not refresh with quota')
  assert(window.pipelineCostFixture.requests.filter(r => r.method === 'POST').length === 1, 'Extra mutation requests')
  return 'Passed: standard/user display, zero, missing data, CPA excluded, no overlap, same quota refresh'
})()
