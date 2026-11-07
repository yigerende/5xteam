(async()=>{
 const f=window.mergeV2Fixture
 if(!f)throw Error('Isolated merge 2.0 fixture required')
 const pause=ms=>new Promise(r=>setTimeout(r,ms))
 const assert=(ok,message)=>{if(!ok)throw Error(message)}
 const button=text=>[...document.querySelectorAll('button')].find(b=>b.textContent.trim()===text)
 const wait=async fn=>{for(let i=0;i<150;i++){if(fn())return;await pause(40)}throw Error('UI wait timed out')}
 const toggle=text=>[...document.querySelectorAll('label.toggle-row')].find(l=>l.querySelector('strong')?.textContent===text)?.querySelector('input')
 ;[...document.querySelectorAll('nav button')].find(b=>b.textContent.includes('空间合并/删除账户')).click()
 await wait(()=>toggle('查额度达标后合并空间2.0'))
 const modes=['查额度达标后自动合并','查额度达标后自动删除','查额度达标后 API 商户','查额度达标后合并空间2.0'].map(toggle)
 assert(modes[3].checked&&modes.slice(0,3).every(m=>!m.checked),'saved mode missing')
 for(const chosen of modes){if(!chosen.checked)chosen.click();await pause(40);assert(modes.filter(m=>m.checked).length===1&&chosen.checked,'four modes not exclusive')}
 button('保存 Pro 配置').click()
 await wait(()=>f.requests.some(r=>r.path==='/api/pro-settings/automation'))
 const saved=f.requests.find(r=>r.path==='/api/pro-settings/automation').body.automation
 assert(saved.auto_merge_v2_enabled&&!saved.auto_merchant_enabled&&!saved.auto_merge_enabled&&!saved.auto_delete_enabled,'wrong saved mode')
 button('测试2.0列表').click()
 await wait(()=>document.querySelector('.merge-v2-flow'))
 assert(document.querySelector('thead').textContent.includes('合并空间2.0状态'),'status column missing')
 const row=[...document.querySelectorAll('tbody tr')].find(r=>r.textContent.includes('pending@example.com'))
 assert(row.querySelector('[data-stage="merge_v2"]')?.textContent.includes('合并2.0'),'final stage missing')
 assert(row.textContent.includes('请求结果待确认'),'uncertainty explanation missing')
 assert(row.querySelector('.merge-v2-flow button').disabled,'unknown request can resume blindly')
 const original=window.confirm;window.confirm=()=>true
 try{
   const submit=row.querySelector('select[aria-label="合并空间2.0提交停用状态"]')
   submit.value='completed';submit.dispatchEvent(new Event('change',{bubbles:true}))
   await wait(()=>f.requests.some(r=>r.path.endsWith('/merge-v2-stage')))
   const edit=f.requests.find(r=>r.path.endsWith('/merge-v2-stage')).body
   assert(edit.stage==='submit'&&edit.status==='completed'&&edit.expected_status==='unknown'&&edit.expected_updated_at,'missing concurrency check')
 }finally{window.confirm=original}
 await pause(100)
 const tab=[...document.querySelectorAll('.space-filter-tabs button')].find(b=>b.textContent.includes('合并空间2.0'))
 tab.click();await wait(()=>f.requests.some(r=>r.path==='/api/pro-accounts'&&r.query.includes('merge_v2')));await pause(80)
 assert(document.querySelector('tbody').textContent.includes('done@example.com')&&!document.querySelector('tbody').textContent.includes('pending@example.com'),'completed filter incorrect')
 const restart=[...document.querySelectorAll('tbody .merge-v2-flow button')].find(b=>b.textContent.includes('重新执行（从邀请开始）'))
 assert(restart&&!restart.disabled,'completed workflow lacks manual restart')
 assert(![...document.querySelectorAll('tbody .merge-v2-flow button')].some(b=>b.textContent.includes('执行 / 续跑')),'completed workflow exposes ordinary continuation')
 return {passed:true,checks:['four exclusive modes and save','independent progress column','last automatic stage','unknown preserves member','versioned manual correction','completed filter and explicit manual restart']}
})()
