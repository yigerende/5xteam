(async () => {
  const f = window.gptPayFixture
  if (!f) throw new Error('Requires isolated GPTPay preview')
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
  const assert = (v, m) => { if (!v) throw new Error(m) }
  const wait = async fn => { for(let i=0;i<240;i++){if(fn())return;await pause(25)}throw new Error('Card limit UI timed out') }
  const button = (text,root=document) => [...root.querySelectorAll('button')].find(b=>b.textContent.trim()===text)
  const field = text => [...document.querySelectorAll('.card-editor label.field')].find(l=>l.querySelector('span')?.textContent===text)?.querySelector('input')
  const set = (text,value) => {const el=field(text);assert(el,'Missing '+text);el.value=value;el.dispatchEvent(new Event('input',{bubbles:true}))}
  f.cards=[{id:'custom-card',name:'测试卡',last4:'4242',enabled:true,exp_year:2099,exp_month:12,opened_accounts:2,pending_accounts:1}]
  button('银行卡信息').click(); await wait(()=>document.querySelector('tbody')?.textContent.includes('2 / 3'))
  button('编辑',document.querySelector('tbody')).click(); await wait(()=>field('最大开通数'))
  assert(field('最大开通数').value==='0','Existing card must inherit global limit')
  set('最大开通数','5');button('保存银行卡').click()
  await wait(()=>document.querySelector('tbody')?.textContent.includes('2 / 5'))
  assert(document.querySelector('tbody').textContent.includes('剩余 2'),'Pending count not deducted')
  assert(document.querySelector('tbody').textContent.includes('单独设置'),'Custom source missing')
  assert(f.requests.findLast(r=>r.path==='/api/gptpay/cards/custom-card').body.max_accounts===5,'Wrong save payload')
  button('禁用',document.querySelector('tbody')).click();await wait(()=>document.querySelector('tbody')?.textContent.includes('已禁用'))
  assert(f.cards[0].max_accounts===5,'Disable erased custom limit')
  button('启用',document.querySelector('tbody')).click();await wait(()=>document.querySelector('tbody')?.textContent.includes('已启用'))
  button('Pro 账号').click();await pause(100);button('银行卡信息').click();await wait(()=>button('编辑',document.querySelector('tbody')))
  button('编辑',document.querySelector('tbody')).click();await wait(()=>field('最大开通数'))
  assert(field('最大开通数').value==='5','Custom limit lost on remount')
  set('最大开通数','1');button('保存银行卡').click();await wait(()=>document.querySelector('tbody')?.textContent.includes('2 / 1'))
  assert(document.querySelector('tbody').textContent.includes('剩余 0'),'Lowering limit shows negative slots')
  button('编辑',document.querySelector('tbody')).click();await wait(()=>field('最大开通数'))
  set('最大开通数','0');button('保存银行卡').click();await wait(()=>document.querySelector('tbody')?.textContent.includes('2 / 3'))
  assert(document.querySelector('tbody').textContent.includes('沿用全局'),'Reset source missing')
  button('添加银行卡').click();await wait(()=>field('最大开通数'))
  set('名称','新卡');set('卡号','4000000000000002');set('到期年份','2099');set('到期月份','12');set('CVV','123');set('最大开通数','8')
  button('保存银行卡').click();await wait(()=>document.querySelector('tbody')?.textContent.includes('0 / 8'))
  assert(f.requests.findLast(r=>r.path==='/api/gptpay/cards'&&r.method==='POST').body.max_accounts===8,'Create missing limit')
  assert(!f.requests.some(r=>r.path.endsWith('/recharge')||r.path.endsWith('/auto-pro')),'Card editor started payment')
  return 'PASS: custom limit create/edit/remount, pending counts, disable/enable, reduce and reset to global; no payments'
})()
