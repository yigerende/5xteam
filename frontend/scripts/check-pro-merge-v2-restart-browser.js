(async()=>{
 const f=window.mergeV2Fixture
 f.showAll=true
 const pause=ms=>new Promise(r=>setTimeout(r,ms))
 const wait=async fn=>{for(let i=0;i<150;i++){if(fn())return;await pause(40)}throw Error('UI wait timed out')}
 const assert=(v,m)=>{if(!v)throw Error(m)}
 const button=text=>[...document.querySelectorAll('button')].find(b=>b.textContent.trim()===text)
 button('测试2.0列表').click()
 await wait(()=>document.querySelector('.merge-v2-flow'))
 for(const state of ['failed','completed','running']){
   const a=f.accounts[0]
   a.pro_workflow_running=state==='running'
   a.pro_auto.status=state
   a.pro_merge_v2.status=state
   a.pro_merge_v2.completed_at=state==='completed'?new Date().toISOString():null
   a.pro_merge_v2.recovery={phase:'cleanup',exhausted:true,attempts:3,cycles:1}
   a.pro_merge_v2_restart=null
   f.remount()
   await pause(300)
   await wait(()=>document.querySelector('.merge-v2-flow'))
   const row=[...document.querySelectorAll('tbody tr')].find(r=>r.textContent.includes(a.email))
   const restart=[...row.querySelectorAll('button')].find(b=>b.textContent.includes('重新执行（从邀请开始）'))
   assert(restart&&!restart.disabled,'restart blocked for '+state)
   const before=f.requests.filter(r=>r.path.endsWith('/merge-v2-restart')).length
   restart.click()
   await wait(()=>f.requests.filter(r=>r.path.endsWith('/merge-v2-restart')).length===before+1)
   const request=f.requests.filter(r=>r.path.endsWith('/merge-v2-restart')).at(-1)
   assert(request.body.request_id.length>8,'missing idempotency id')
   await wait(()=>document.body.textContent.includes('等待旧任务退出后从邀请开始'))
 }
 button('测试2.0设置').click()
 await wait(()=>[...document.querySelectorAll('nav button')].some(b=>b.textContent.includes('空间合并/删除账户')))
 ;[...document.querySelectorAll('nav button')].find(b=>b.textContent.includes('空间合并/删除账户')).click()
 await pause(200)
 const field=name=>[...document.querySelectorAll('label.field')].find(l=>l.querySelector('span')?.textContent===name)?.querySelector('input')
 assert(field('提交前网络故障额外重试次数')?.value==='5','network retry default missing')
 assert(field('网络故障重试间隔（秒）')?.value==='60','network delay missing')
 return {passed:true,checks:['restart after exhaustion','restart completed round','restart while old worker active','idempotency ids','pending handoff feedback','network retry settings']}
})()