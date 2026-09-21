(async()=>{
 const fixture=window.gptPayFixture
 if(!fixture)throw new Error('Requires isolated Pro preview')
 const pause=ms=>new Promise(r=>setTimeout(r,ms)),assert=(v,m)=>{if(!v)throw new Error(m)}
 const wait=async(fn,msg='Timed out')=>{for(let i=0;i<600;i++){if(fn())return;await pause(25)}throw new Error(msg)}
 const button=text=>[...document.querySelectorAll('button')].find(b=>b.textContent.trim()===text)
 const state=stage=>document.querySelector('.auto-flow-cell [data-stage="'+stage+'"]')
 const refresh=async()=>{button('推送设置').click();await pause(50);button('Pro 账号').click();await pause(150)}
 const choose=async title=>{await wait(()=>document.querySelector('[title="更多操作"]'));document.querySelector('[title="更多操作"]').click();await wait(()=>document.querySelector('.pro-action-menu'));const b=document.querySelector('.pro-action-menu [title="'+title+'"]');assert(b&&!b.disabled,'Unavailable: '+title);b.click();await pause(25)}
 const running=async stage=>{await wait(()=>state(stage)?.classList.contains('running'),'Not running: '+stage);const spinner=state(stage).querySelector('.spin');assert(spinner,'No spinner: '+stage);assert(getComputedStyle(spinner).animationName!=='none','Spinner not animated: '+stage)}
 const completed=async stage=>{await wait(()=>state(stage)?.classList.contains('completed'),'Not completed: '+stage);assert(state(stage).textContent.includes('已完成'),'Wrong completion label: '+stage)}
 fixture.persistence=true
 fixture.cards=[{id:'manual-card',name:'手动卡',enabled:true,last4:'4242',exp_year:2099,exp_month:12}]
 await choose('获取临时 AT')
 await wait(()=>button('确认获取'))
 assert(!state('login').classList.contains('running'),'Opening confirmation started login')
 button('确认获取').click();await running('login');await completed('login')
 await wait(()=>button('关闭'));button('关闭').click();await pause(50)
 await choose('开通 Pro');await wait(()=>button('确认开通 Pro 5x')&&!button('确认开通 Pro 5x').disabled)
 button('确认开通 Pro 5x').click();await running('recharge')
 await wait(()=>document.querySelector('.pro-recharge-dialog h3')?.textContent.includes('开通中'))
 assert(!state('recharge').classList.contains('completed'),'Pending payment shown as completed')
 button('查询状态').click();await completed('recharge');await wait(()=>!button('关闭').disabled);button('关闭').click();await pause(50)
 await choose('登录获取 RT / AT');await wait(()=>button('确认获取'));button('确认获取').click();await running('oauth');await completed('oauth')
 await wait(()=>button('关闭'));button('关闭').click();await pause(50)
 fixture.failNext='push'
 await choose('推送当前下游');await running('push');await wait(()=>state('push').classList.contains('failed'),'Push failure invisible')
 assert(state('push').title.includes('failure'),'Failure reason missing')
 await choose('推送当前下游');await running('push');await completed('push')
 await choose('查询额度');await running('quota');await completed('quota')
 await choose('执行或续跑空间合并');await running('merge')
 await wait(()=>document.querySelector('.merge-stage.running'))
 assert(!state('merge').classList.contains('completed'),'Unfinished merge shown complete')
 await completed('merge')
 assert(document.querySelectorAll('.merge-stage.completed').length===4,'Four-step states not completed')
 await refresh()
 assert(document.querySelectorAll('.auto-flow-cell .completed').length===6,'List reload lost manual stages')
 assert(!fixture.profile.pro_auto?.id,'Manual buttons started automatic flow')
 assert(!fixture.requests.some(r=>r.path.endsWith('/auto-pro')&&r.method==='POST'),'Manual flow submitted automation')
 fixture.persist()
 return 'PASS: six manual actions, immediate animated spinners, payment processing, completed stages, failure and retry, four-step merge, list reload, no automatic launch'
})()
