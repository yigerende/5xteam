// Run with agent-browser eval --stdin on pipeline-cost-preview.html?admin.
(() => {
  if (!window.pipelineCostFixture) throw new Error('Requires isolated cost fixture')
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const cells = [...document.querySelectorAll('.cost-cell')]
  assert(cells.length === 3, 'Missing mother account cost rows')
  const expected = ['用户 $52.0400', '用户 $0.0000', '用户 --']
  cells.forEach((cell, i) => {
    assert(cell.querySelector('strong').textContent === '$25.1788', 'Standard cost changed')
    assert(cell.querySelector('.user-cost').textContent === expected[i], 'User cost display mismatch')
    assert(cell.querySelector('.user-cost').title === '累计用户消耗额度', 'Missing user cost tooltip')
    const blocks = [...cell.children].map(el => el.getBoundingClientRect())
    assert(blocks.every((rect, j) => !j || rect.top >= blocks[j - 1].bottom), 'Cost lines overlap')
  })
  assert(window.pipelineCostFixture.requests.every(r => r.method === 'GET' && ['/api/admin-accounts', '/api/admin-capacity-snapshots'].includes(r.path)), 'Unexpected remote request')
  cells[0].scrollIntoView({ block: 'center', inline: 'center' })
  return 'Passed: mother standard/user costs, zero, unknown, line layout, local-only reads'
})()
