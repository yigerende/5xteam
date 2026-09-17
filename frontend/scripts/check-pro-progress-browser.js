// Execute on the isolated Pro preview. No real accounts or network workflows are used.
(async () => {
 if (!window.mailExportFixture) throw new Error('Requires preview fixture')
 const fixture = window.mailExportFixture
 const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
 const assert = (ok, message) => { if (!ok) throw new Error(message) }
 const wait = async fn => { for (let i=0;i<450;i++) { if(fn()) return; await pause(25) }; throw new Error('Progress timeout') }
 const row = email => [...document.querySelectorAll('tbody tr')].find(el => el.textContent.includes(email))
 const stage = (email, key) => row(email)?.querySelector('[data-stage="'+key+'"]')
 const mergeButton = email => row(email).querySelector('[title="执行或续跑空间合并"]')
 const email = 'fixture-01@example.com'
 fixture.mergeStepDelay = 2200
 const seen = new Set()
 const observer = new MutationObserver(() => {
  for(const key of ['invite','accept','transfer','remove'])if(stage(email,key)?.classList.contains('running')) seen.add(key)
 })
 observer.observe(document.body,{subtree:true,attributes:true,childList:true})
 try {
  const before = fixture.requests.filter(r=>r.path==='/api/pro-accounts').length
  mergeButton(email).click()
  await pause(50)
  assert(mergeButton(email).disabled && row(email).querySelector('.merge-activity'), 'Immediate starting feedback missing')
  await wait(()=>stage(email,'remove')?.classList.contains('completed'))
  assert(seen.size===4, 'Not all running stages rendered: '+[...seen])
  assert(fixture.requests.filter(r=>r.path==='/api/pro-accounts').length>before+1,'List did not poll while POST pending')
  assert(mergeButton(email).disabled,'Completed merge must not run again')
  for(const key of ['invite','accept','transfer','remove'])assert(stage(email,key).textContent.includes('成功'),'Success text missing')
 } finally { observer.disconnect() }
 const retryEmail='fixture-04@example.com'
 fixture.mergeStepDelay=100
 fixture.mergeFailStage='transfer'
 mergeButton(retryEmail).click()
 await wait(()=>stage(retryEmail,'transfer')?.classList.contains('failed') && !mergeButton(retryEmail).disabled)
 assert(stage(retryEmail,'invite').classList.contains('completed')&&stage(retryEmail,'accept').classList.contains('completed'),'Failure lost completed stages')
 fixture.mergeFailStage=''
 const callCount=fixture.mergeCalls.length
 mergeButton(retryEmail).click()
 await wait(()=>stage(retryEmail,'remove')?.classList.contains('completed'))
 assert(fixture.mergeCalls.slice(callCount).join(',')==='transfer,remove','Resume repeated completed steps')
 // Simulate an automatic/background workflow without clicking the action button.
 const automatic='fixture-07@example.com'
 fixture.mergeStepDelay=1800
 const background=fetch('/api/pro-accounts/'+encodeURIComponent(automatic)+'/merge',{method:'POST'})
 await wait(()=>row(automatic)?.querySelector('.merge-activity'))
 await background
 await wait(()=>stage(automatic,'remove')?.classList.contains('completed'))
 assert(mergeButton(automatic).disabled,'Background merge completion not reflected')
 return 'Passed: immediate feedback, four live stages, success, failure, resume, completed disabled, background polling'
})()
