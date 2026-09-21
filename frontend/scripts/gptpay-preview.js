import { createApp, h } from 'vue'
import ProManagementView from '../src/components/ProManagementView.vue'
import '../src/styles.css'

// Isolated browser fixture: all requests are intercepted; no real AT/card or
// upstream service is used. This module is not an entry in the production build.
const fixture = window.gptPayFixture = { requests: [], cards: [], orders: [], config: { url:'https://gptpay.tokenseek.app/api/v1',plan_code:'pro5',key_present:true,session_timeout_minutes:30 }, pro: {provider:'sub2',quota_enabled:false,quota_used_threshold:100,quota_check_interval_seconds:120,auto_merge_enabled:false,target_admin_id:'mother-8',target_seat_type:'default',concurrency:2,retry_count:2,retry_interval_seconds:3} }
const profile = { email:'fixture-pro@example.com',access_token_present:true,refresh_token_present:true,management_scope:'pro',current_plan_type:'free' }
Object.assign(fixture.config,{card_account_limit:3});Object.assign(fixture.pro,{scheduled_enabled:false,max_unmerged:5,schedule_interval_seconds:120})
fixture.scheduleRuns=[];fixture.imported=new Set()
fixture.profile=profile
const persisted = sessionStorage.getItem('pro-manual-preview')
if (persisted) { const saved = JSON.parse(persisted); Object.assign(profile, saved.profile); fixture.orders = saved.orders; fixture.cards = saved.cards }
fixture.persist = () => sessionStorage.setItem('pro-manual-preview', JSON.stringify({ profile, orders:fixture.orders, cards:fixture.cards }))
const jobs = {}
const stageState = (stage, status, error='') => {
  profile.pro_stage_progress ||= {steps:{},errors:{}}
  profile.pro_stage_progress.steps[stage]=status
  profile.pro_stage_progress.errors[stage]=error
  if (fixture.persistence) fixture.persist()
}
const delay = ms=>new Promise(resolve=>setTimeout(resolve,ms))
const runStage = async (stage) => {
  stageState(stage,'running')
  await delay(650)
  const fail=fixture.failNext===stage
  fixture.failNext=''
  stageState(stage,fail?'failed':'completed',fail?'fixture '+stage+' failure':'')
  if(stage==='push'&&!fail){profile.push_status='completed';profile.push_provider='sub2'}
  return fail
}
const reply = (data, status=200) => new Response(JSON.stringify({ok:status<400,data,error:status>=400?'Fixture rejected request':''}), {status,headers:{'Content-Type':'application/json'}})
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin)
  if (!url.pathname.startsWith('/api/')) throw new Error('Fixture prohibits external requests')
  const method=options.method || 'GET', body=options.body?JSON.parse(options.body):{}
  fixture.requests.push({path:url.pathname,method,body})
  await new Promise(resolve=>setTimeout(resolve,30))
  if(url.pathname==='/api/pro-accounts/delete'){
    await delay(250)
    const items=body.emails.map(email=>{
      const account=(fixture.listProfiles||[profile]).find(p=>p.email===email)
      if(!account)return {email,deleted:false,error:'Pro 账号不存在'}
      if(account.pro_workflow_running)return {email,deleted:false,error:'账号正在执行空间合并'}
      fixture.listProfiles=(fixture.listProfiles||[profile]).filter(p=>p.email!==email)
      return {email,deleted:true}
    })
    return reply({items,total:items.length,succeeded:items.filter(i=>i.deleted).length,failed:items.filter(i=>!i.deleted).length})
  }
  if(url.pathname==='/api/pro-schedule/run'){
    const run={id:'run-'+fixture.scheduleRuns.length,trigger:'manual',status:'running',started_at:new Date().toISOString(),tasks:[{email:profile.email,status:'running',stage:'login',steps:{login:'running'}}]};fixture.scheduleRuns.unshift(run)
    void delay(1000).then(()=>{run.status='completed';run.message='已推送 Sub2 并成功刷新额度';run.tasks[0]={email:profile.email,status:'completed',stage:'quota',steps:{login:'completed',recharge:'completed',oauth:'completed',push:'completed',quota:'waiting'}}})
    return reply({active:run})
  }
  if(url.pathname==='/api/pro-schedule')return reply({enabled:fixture.pro.scheduled_enabled,opened_unmerged:4,pending:0,maximum:fixture.pro.max_unmerged,needed:1,card_capacity:2,active:fixture.scheduleRuns.find(r=>r.status==='running'),server_time:new Date().toISOString(),next_check_at:new Date(Date.now()+120000).toISOString()})
  if(url.pathname==='/api/pro-schedule/runs')return reply({items:fixture.scheduleRuns,total:fixture.scheduleRuns.length})
  if(url.pathname.endsWith('/events'))return reply({events:[],has_more:false})
  if(url.pathname==='/api/pro-accounts/export'){
    const data={format:'space-pro-accounts',version:1,exported_at:new Date().toISOString(),accounts:[{profile:{...profile,management_scope:'pro'},credentials:{email:profile.email,access_token:'fixture-at',refresh_token:'fixture-rt',chatgpt_session:'{}'},orders:[]}]}
    fixture.lastExport=data
    return new Response(JSON.stringify(data),{status:200,headers:{'Content-Type':'application/json','Content-Disposition':'attachment; filename="pro-fixture.json"'}})
  }
  if(url.pathname==='/api/pro-accounts/import'){
    await delay(200)
    const results=body.file.accounts.map(a=>{const exists=fixture.imported.has(a.profile.email);fixture.imported.add(a.profile.email);return {email:a.profile.email,status:exists?'skipped':'imported',message:exists?'相同快照已导入':'进度已保存'}})
    profile.pro_migration={paused:true,notice:'迁移待续跑'}
    return reply({results,total:results.length})
  }
  if(url.pathname.endsWith('/resume-import')){profile.pro_migration.paused=false;return reply(profile,202)}
  if (url.pathname.startsWith('/api/mail/accounts/')) {
    const stage=url.pathname.endsWith('/oauth')?'oauth':'login',id=stage+'-job'
    stageState(stage,'running')
    const job=jobs[id]={job_id:id,status:'running',logs:[]}
    void delay(650).then(()=>{stageState(stage,'completed');profile.access_token_present=true;if(stage==='oauth')profile.refresh_token_present=true;job.status='success'})
    return reply({job})
  }
  if(url.pathname.startsWith('/api/mail/login/')||url.pathname.startsWith('/api/mail/oauth/'))return reply({job:jobs[url.pathname.split('/').at(-1)]})
  if (url.pathname.endsWith('/oauth/refresh')) { const fail=await runStage('oauth');return reply(profile,fail?400:200) }
  if(url.pathname.endsWith('/push')||url.pathname.endsWith('/quota')) {
    const stage=url.pathname.split('/').at(-1),fail=await runStage(stage)
    return reply(profile,fail?400:200)
  }
  if(url.pathname.endsWith('/merge')){
    stageState('merge','running');profile.pro_workflow_running=true
    void(async()=>{
      for(const step of ['invite','accept','transfer','remove']){
        profile['pro_'+step+'_status']='running';await delay(500);profile['pro_'+step+'_status']='completed'
      }
      profile.pro_workflow_running=false;profile.space_merged_once=true;stageState('merge','completed')
    })()
    return reply(profile,202)
  }
  if(url.pathname==='/api/pro-settings')return reply(fixture.pro)
  if(url.pathname==='/api/pro-settings/automation'){fixture.pro={...fixture.pro,...body.automation};fixture.config={...fixture.config,...body.gptpay};return reply({automation:fixture.pro,gptpay:fixture.config})}
  if(url.pathname==='/api/pro-accounts'){
    if(fixture.listProfiles)return reply({items:fixture.listProfiles,total:fixture.listProfiles.length,summary:{all:fixture.listProfiles.length,oauth_ready:0,pushed:0,merged:0}})
    const paid=fixture.orders.find(o=>o.email===profile.email&&o.status==='success')
    return reply({items:[{...profile,activation_card:paid?{card_name:paid.card_name,card_last4:paid.card_last4}:null}],total:1,summary:{all:1,oauth_ready:1}})
  }
  if(url.pathname==='/api/gptpay/settings'){if(method==='PUT')fixture.config={...fixture.config,...body,key_present:true};return reply(fixture.config)}
  if(url.pathname.endsWith('/auto-pro/stop')){profile.pro_auto.status='interrupted';profile.pro_auto.error='用户已停止';return reply(profile)}
  if(url.pathname.endsWith('/auto-pro')){
    if(method==='GET')return reply(profile.pro_auto||{})
    if(profile.pro_auto?.status==='running')return reply(profile)
    delete profile.pro_stage_progress
    const state=profile.pro_auto={id:'fixture-auto',status:'running',stage:'login',steps:{login:'running'}}
    void(async()=>{
      for(const step of ['login','recharge','oauth','push','quota','merge']){
        if(state.status!=='running')return
        state.stage=step;state.steps[step]='running'
        await new Promise(resolve=>setTimeout(resolve,500))
        if(step==='login')profile.access_token_present=true
        if(step==='merge'){for(const mergeStep of ['invite','accept','transfer','remove']){profile['pro_'+mergeStep+'_status']='running';await new Promise(resolve=>setTimeout(resolve,250));profile['pro_'+mergeStep+'_status']='completed'}}
        if(state.status!=='running')return
        state.steps[step]='completed'
      }
      state.status='completed';profile.space_merged_once=true
    })()
    return reply(profile)
  }
  if(url.pathname==='/api/gptpay/cards'){
    if(method==='POST'){const card={id:'card-'+(fixture.cards.length+1),name:body.name,last4:body.number.slice(-4),exp_year:body.exp_year,exp_month:body.exp_month,enabled:body.enabled};fixture.cards.push(card);return reply(card)}
    const filtered=fixture.cards.map(c=>({...c,opened_accounts:c.opened_accounts||0,pending_accounts:c.pending_accounts||0,account_limit:fixture.config.card_account_limit,remaining_accounts:Math.max(0,fixture.config.card_account_limit-(c.opened_accounts||0)-(c.pending_accounts||0))})).filter(c=>url.searchParams.get('enabled')!=='true'||c.enabled&&c.remaining_accounts>0), size=Number(url.searchParams.get('page_size')||10), page=Number(url.searchParams.get('page')||1)
    return reply({items:filtered.slice((page-1)*size,page*size),total:filtered.length})
  }
  if(url.pathname.startsWith('/api/gptpay/cards/')){const id=url.pathname.split('/').at(-1),card=fixture.cards.find(c=>c.id===id);if(method==='DELETE'){fixture.cards=fixture.cards.filter(c=>c.id!==id);return reply({deleted:true})};Object.assign(card,body);return reply(card)}
  if(url.pathname==='/api/gptpay/orders')return reply({items:fixture.orders,total:fixture.orders.length})
  if(url.pathname.endsWith('/recharge')){stageState('recharge','running');await delay(650);const card=fixture.cards.find(c=>c.id===body.card_id);const order={id:'pro-'+body.request_id,email:profile.email,card_name:card.name,card_last4:card.last4,plan_code:body.plan_code,status:'processing',remote:{id:'supplier-fixture',orderNo:'GPT-TEST',status:'processing',cancellationStatus:'waiting',settlementStatus:'reserved'},created_at:new Date().toISOString(),updated_at:new Date().toISOString()};fixture.orders.unshift(order);return reply(order)}
  if(url.pathname.endsWith('/refresh')){stageState('recharge','completed');Object.assign(fixture.orders[0],{status:'success',remote:{...fixture.orders[0].remote,status:'success',cancellationStatus:'success',settlementStatus:'settled',chargedCredits:100},updated_at:new Date().toISOString()});return reply(fixture.orders[0])}
  if(url.pathname==='/api/gptpay/account')return reply({user:{email:'supplier@example.com'},wallet:{availableCredits:10000,reservedCredits:100},level:{name:'默认'},plans:[{planCode:'pro5',successCredits:100,failureCredits:5},{planCode:'pro20',successCredits:200,failureCredits:5}],apiKey:{expiresAt:null}})
  if(url.pathname==='/api/gptpay/orders/query')return reply({orders:body.order_ids.map(id=>({id,status:id==='supplier-fixture'?'success':'not_found'}))})
  return reply(null,404)
}
createApp({render:()=>h('main',{style:'padding:28px;max-width:1650px;margin:auto'},[h(ProManagementView,{defaultPageSize:10,adminAccounts:[{id:'mother-8',label:'母号8',email:'mother@example.com',team_account_id:'fixture-team'}]})])}).mount('#app')
