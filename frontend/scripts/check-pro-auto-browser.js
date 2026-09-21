(async()=>{
 const fixture=window.gptPayFixture
 if(!fixture)throw new Error('Isolated GPTPay preview required')
 const pause=ms=>new Promise(r=>setTimeout(r,ms)),assert=(v,m)=>{if(!v)throw new Error(m)}
 const wait=async f=>{for(let i=0;i<500;i++){if(f())return;await pause(25)}throw new Error('Auto UI timed out')}
 const button=text=>[...document.querySelectorAll('button')].find(b=>b.textContent.trim()===text)
 const tab=async text=>{button(text).click();await pause(150)}
 fixture.cards=[{id:'auto-card',name:'自动卡',enabled:true,last4:'4242',exp_year:2099,exp_month:12}]
 fixture.profile.access_token_present=false
 await tab('Pro 账号')
 await wait(()=>document.querySelector('[title="更多操作"]'))
 document.querySelector('[title="更多操作"]').click();await wait(()=>document.querySelector('.pro-action-menu'))
 const open=document.querySelector('[title="全自动开通 Pro"]');assert(!open.disabled,'No AT must not disable automatic login');open.click()
 await wait(()=>button('确认全自动开通 Pro 20x')&&!button('确认全自动开通 Pro 20x').disabled)
 button('确认全自动开通 Pro 20x').click();button('确认全自动开通 Pro 20x').click()
 await wait(()=>document.querySelector('.pro-auto-dialog .running'))
 assert(fixture.requests.filter(r=>r.path.endsWith('/auto-pro')&&r.method==='POST').length===1,'Duplicate auto start')
 await wait(()=>document.querySelector('.pro-auto-dialog .done'))
 assert(document.querySelectorAll('.pro-auto-dialog .pro-auto-stages>.completed').length===6,'Not all six stages complete')
 button('关闭').click();await wait(()=>document.querySelectorAll('.auto-flow-cell .completed').length===6)
 assert(document.querySelectorAll('.merge-stage.completed').length===4,'Four merge statuses not updated')
 const headings=[...document.querySelectorAll('.pro-table th')].map(e=>e.textContent.trim())
 assert(headings.indexOf('全自动阶段')+1===headings.indexOf('四步状态'),'Wrong phase column position')
 document.querySelector('[title="更多操作"]').click();await wait(()=>document.querySelector('.pro-action-menu'))
 document.querySelector('[title="全自动开通 Pro"]').click();await wait(()=>document.querySelector('.pro-auto-dialog .done'))
 assert(!button('确认全自动开通 Pro 20x'),'Completed flow can repurchase')
 return 'Passed: no-AT automatic entry, one launch, live stages, completed recovery, six stages before four merge states'
})()
