(async()=>{
 const f=window.gptPayFixture;if(!f)throw new Error('Isolated fixture required')
 const pause=ms=>new Promise(r=>setTimeout(r,ms)),assert=(v,m)=>{if(!v)throw new Error(m)}
 const wait=async(fn,msg)=>{for(let i=0;i<600;i++){if(fn())return;await pause(20)}throw new Error(msg||'UI timeout')}
 const button=(text,root=document)=>[...root.querySelectorAll('button')].find(b=>b.textContent.trim()===text)
 const click=async text=>{const b=button(text);assert(b&&!b.disabled,'Missing/enabled button '+text);b.click();await pause(100)}
 const field=text=>[...document.querySelectorAll('label.field')].find(l=>l.querySelector('span')?.textContent.trim()===text)?.querySelector('input,select')
 const fill=(text,value)=>{const e=field(text);assert(e,'No field '+text);e.value=value;e.dispatchEvent(new Event('input',{bubbles:true}))}
 await click('Pro 全自动配置');await wait(()=>field('每张银行卡最多开通账号数'))
 assert(field('每张银行卡最多开通账号数').value==='3','Default card max')
 fill('开通并在 Sub 最大数（未合并）',5);fill('定时开通检查间隔（秒）',60)
 const toggle=[...document.querySelectorAll('.toggle-row')].find(l=>l.textContent.includes('定时执行全自动 Pro')).querySelector('input');toggle.click()
 await click('保存 Pro 配置');await wait(()=>f.pro.scheduled_enabled===true)
 assert(f.pro.schedule_interval_seconds===60&&f.config.card_account_limit===3,'Config not persisted')
 f.cards=[{id:'full',name:'满额卡',last4:'1111',enabled:true,exp_year:2099,exp_month:12,opened_accounts:3},{id:'free',name:'有余卡',last4:'4242',enabled:true,exp_year:2099,exp_month:12,opened_accounts:1,pending_accounts:1}]
 await click('银行卡信息');await wait(()=>document.body.innerText.includes('满额卡'))
 assert(document.body.innerText.includes('3 / 3')&&document.body.innerText.includes('剩余 1'),'Card usage not displayed')
 await click('Pro 账号');await wait(()=>document.querySelector('[title="更多操作"]'))
 document.querySelector('[title="更多操作"]').click();await wait(()=>document.querySelector('.pro-action-menu'))
 document.querySelector('[title="开通 Pro"]').click();await wait(()=>document.querySelector('.pro-recharge-dialog select option[value="free"]'))
 assert(!document.querySelector('.pro-recharge-dialog select option[value="full"]'),'Full card still selectable')
 await wait(()=>button('取消')&&!button('取消').disabled);await click('取消')
 await click('Pro 执行记录');await wait(()=>button('立即检查并开通'))
 await click('立即检查并开通');await wait(()=>document.querySelector('.run-task .running'),'No running task stage')
 await wait(()=>document.body.innerText.includes('已推送 Sub2 并成功刷新额度'),'No final run result')
 assert(button('完整日志'),'Missing detailed log entry')
 await click('Pro 账号');await pause(150)
 const rowCheck=document.querySelector('.pro-table tbody input[type="checkbox"]');assert(rowCheck,'No account selection');if(!rowCheck.checked)rowCheck.click();await pause(50)
 await click('导入 / 导出');await wait(()=>button('下载 JSON'))
 let blob;const original=URL.createObjectURL;URL.createObjectURL=v=>{blob=v;return original(v)}
 try{await click('下载 JSON');await wait(()=>blob);const data=JSON.parse(await blob.text());assert(data.accounts[0].credentials.refresh_token==='fixture-rt','Export lost credentials');assert(f.requests.filter(r=>r.path==='/api/pro-accounts/export').at(-1).body.emails.length===1,'Export ignored selection')}finally{URL.createObjectURL=original}
 await click('导入 JSON')
 const input=document.querySelector('.pro-transfer-dialog input[type="file"]')
 const upload=raw=>{const dt=new DataTransfer();dt.items.add(new File([raw],'migration.json',{type:'application/json'}));input.files=dt.files;input.dispatchEvent(new Event('change',{bubbles:true}))}
 upload('{broken');await wait(()=>document.querySelector('.pro-transfer-dialog [role="alert"]'))
 assert(button('开始导入').disabled,'Invalid file accepted')
 const file={format:'space-pro-accounts',version:1,exported_at:new Date().toISOString(),accounts:Array.from({length:21},(_,i)=>({profile:{email:`test${i}@example.com`,management_scope:'pro'},credentials:{email:`test${i}@example.com`},orders:[]}))}
 upload(JSON.stringify(file));await wait(()=>button('开始导入')?.disabled===false)
 await click('开始导入');await wait(()=>document.querySelector('progress')?.value===20,'No intermediate import progress')
 await wait(()=>document.querySelector('progress')?.value===21,'No finished import progress')
 assert(f.requests.filter(r=>r.path==='/api/pro-accounts/import').length===2,'Import not bounded in batches')
 await wait(()=>button('开始导入')?.disabled===false);await click('开始导入');await wait(()=>button('开始导入')?.disabled===false&&document.querySelector('.transfer-progress').textContent.includes('跳过 21'))
 await click('关闭');await wait(()=>document.body.innerText.includes('迁移待续跑'))
 document.querySelector('[title="更多操作"]').click();await wait(()=>document.querySelector('.pro-action-menu'))
 assert(document.querySelector('[title="继续迁移流程"]'),'No explicit migration continuation')
 return 'Passed: scheduled config, card counts/full-card exclusion, run progress/log entry, selected credential JSON export, invalid JSON, 21-account import progress, duplicate replay, migration continuation'
})()
