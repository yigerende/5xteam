// Isolated preview only; all requests are handled by the fixture.
(async () => {
 const f=window.mailExportFixture
 if (!f?.items) throw new Error('Requires preview fixture')
 const assert=(ok,text)=>{if(!ok)throw new Error(text)}
 const wait=async fn=>{for(let i=0;i<400;i++){if(fn())return;await new Promise(r=>setTimeout(r,25))}throw new Error('UI timeout')}
 const email=f.items[0].email
 const row=()=>[...document.querySelectorAll('tbody tr')].find(el=>el.textContent.includes(email))
 const merge=()=>row().querySelector('[title="执行或续跑空间合并"]')
 const select=()=>row().querySelector('[data-stage="transfer"] select')
 f.mergeStepDelay=1800
 merge().click()
 await wait(()=>document.body.innerText.includes('已提交后台'))
 assert(!document.body.innerText.includes('操作完成'),'202 receipt claimed completion')
 assert(merge().disabled,'Active account accepts duplicates')
 await wait(()=>!document.querySelector('.heading-actions button').disabled)
 assert(merge().disabled,'Page controls must be released while the account is still running')
 await wait(()=>row().querySelector('[data-stage="remove"]').classList.contains('completed'))
 await wait(()=>document.body.innerText.includes('四步流程完成'))
 Object.assign(f.items[0],{pro_transfer_status:'unknown',pro_remove_status:'pending',pro_workflow_running:false,pro_last_error:'合并结果待确认',space_merged_once:false})
 await wait(()=>select().value==='unknown')
 assert(select().selectedOptions[0].textContent==='待确认','Unknown status mislabeled')
 assert(merge().disabled,'Ambiguous merge can be repeated')
 const original=window.confirm
 window.confirm=()=>true
 try{
  const count=f.mergeCalls.length
  select().value='completed'
  select().dispatchEvent(new Event('change',{bubbles:true}))
  await wait(()=>!merge().disabled)
  merge().click()
  await wait(()=>row().querySelector('[data-stage="remove"]').classList.contains('completed'))
  assert(f.mergeCalls.slice(count).join(',')==='remove','Correction repeated remote merge')
 }finally{window.confirm=original}
 return 'Passed: 202 receipt, independent page controls, background completion, unknown state, duplicate guard, correction resumes removal only'
})()
