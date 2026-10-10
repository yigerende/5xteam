(async () => {
  const f = window.gptPayFixture
  if (!f) throw new Error('Requires isolated GPTPay preview')
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (v, msg) => { if (!v) throw new Error(msg) }
  const wait = async fn => { for (let i=0;i<320;i++) { if(fn()) return; await pause(25) } throw new Error('Filter UI timed out') }
  const tab = label => [...document.querySelectorAll('.space-filter-tabs button')].find(b => b.textContent.trim().startsWith(label))
  const button = label => [...document.querySelectorAll('button')].find(b => b.textContent.trim()===label)
  const rows = () => [...document.querySelectorAll('.pro-table .account-cell strong')].map(el=>el.textContent)
  f.filterCases = [
    {state:'unmerged',profile:{email:'new@example.com'}},
    {state:'in_progress',profile:{email:'quota@example.com',pro_auto:{id:'quota',status:'waiting_quota',steps:{login:'completed',recharge:'completed',oauth:'completed',push:'completed',quota:'waiting'}}}},
    {state:'in_progress',profile:{email:'failed@example.com',pro_auto:{id:'failed',status:'failed',steps:{login:'failed'}}}},
    {state:'merged',profile:{email:'merged@example.com',space_merged_once:true}}
  ]
  tab('全部').click(); await wait(()=>rows().length===4)
  assert(tab('未空间合并').textContent.trim()==='未空间合并 1','Wrong untouched count')
  assert(tab('流程中').textContent.trim()==='流程中 2','Wrong progress count')
  assert(tab('未空间合并').nextElementSibling===tab('流程中'),'New filter must follow unmerged')
  document.querySelector('.pro-table tbody input[type=checkbox]').click()
  await wait(()=>[...document.querySelectorAll('button')].some(b=>b.textContent.includes('批量删除')&&!b.disabled))
  tab('未空间合并').click();await wait(()=>rows().length===1&&rows()[0]==='new@example.com')
  assert(!document.querySelector('.pro-table tbody input[type=checkbox]').checked,'Selection survived filter change')
  tab('流程中').click();await wait(()=>rows().length===2&&rows().includes('quota@example.com'))
  assert(document.querySelector('.pro-table tbody').textContent.includes('等待额度'),'Waiting quota not visible')
  const search=document.querySelector('input[placeholder="搜索账号"]')
  search.value='failed';search.dispatchEvent(new Event('input',{bubbles:true}))
  await wait(()=>rows().length===1&&rows()[0]==='failed@example.com')
  assert(tab('流程中').textContent.includes('2'),'Search overwrote global count')
  button('导入 / 导出').click();await wait(()=>button('下载 JSON'))
  button('下载 JSON').click();await wait(()=>f.requests.some(r=>r.path==='/api/pro-accounts/export'))
  const exported=f.requests.findLast(r=>r.path==='/api/pro-accounts/export').body
  assert(exported.all===true&&exported.merge_state==='in_progress'&&exported.query==='failed','Export did not carry filter/search')
  await wait(()=>button('关闭')&&!button('关闭').disabled);button('关闭').click()
  search.value='';search.dispatchEvent(new Event('input',{bubbles:true}));await wait(()=>rows().length===2)
  f.filterCases[1].profile.space_merged_once=true;f.filterCases[1].state='merged'
  tab('流程中').click();await wait(()=>rows().length===1&&rows()[0]==='failed@example.com')
  tab('已空间合并').click();await wait(()=>rows().length===2&&rows().includes('quota@example.com'))
  assert(tab('已空间合并').textContent.includes('2'),'Merge completion did not update count')
  tab('全部').click();await wait(()=>rows().length===4)
  return 'PASS: filter order/counts, untouched vs waiting/failed, selection reset, search, filtered export, merge transition'
})()
