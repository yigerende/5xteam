import {createApp,h,ref} from 'vue'
import ProGPTPayPanel from '../src/components/ProGPTPayPanel.vue'
import ProManagementView from '../src/components/ProManagementView.vue'
import '../src/styles.css'

const now=new Date().toISOString()
const fixture=window.mergeV2Fixture={
  requests:[],settings:{provider:'sub2',sub2:{group_ids:[],models:[]},cpa:{},vm2api:{},auto_merge_enabled:false,auto_delete_enabled:false,auto_merchant_enabled:false,auto_merge_v2_enabled:true,merchant_key_present:true,deletion_check_interval_seconds:300,quota_used_threshold:90,quota_check_interval_seconds:120,merge_concurrency:2,concurrency:2,retry_count:2,retry_interval_seconds:3,target_seat_type:'default',max_unmerged:5,schedule_interval_seconds:120,purchase_windows:[],ejh_plan_templates:{},nexovcc_plan_templates:{}},
  pay:{provider:'tokenseek',url:'https://supplier.invalid',plan_code:'pro5',country:'US',key_present:true,session_timeout_minutes:30,card_account_limit:3,refresh_interval_seconds:60},
  accounts:[{email:'pending@example.com',updated_at:now,management_scope:'pro',pro_auto:{id:'pending',status:'interrupted',stage:'merge_v2'},pro_stage_progress:{steps:{login:'completed',recharge:'completed',oauth:'completed',push:'completed',quota:'completed',merge_v2:'unknown'}},pro_merge_v2:{status:'unknown',stage:'submit',steps:{invite:'completed',login:'completed',submit:'unknown'},started_at:now,submitted_at:now,error:'请求结果待确认'}},{email:'done@example.com',updated_at:now,management_scope:'pro',pro_auto:{id:'done',status:'completed',stage:'merge_v2'},pro_stage_progress:{steps:{merge_v2:'completed'}},pro_merge_v2:{status:'completed',stage:'groups',steps:{invite:'completed',login:'completed',submit:'completed',remove:'completed',groups:'completed'},started_at:now,accepted_at:now,completed_at:now}}],
}
const reply=data=>new Response(JSON.stringify({ok:true,data}),{headers:{'Content-Type':'application/json'}})
window.fetch=async(path,options={})=>{
  const url=new URL(path,location.origin),body=options.body?JSON.parse(options.body):null
  if(url.origin!==location.origin || !url.pathname.startsWith('/api/'))throw Error('External requests prohibited in fixture')
  fixture.requests.push({path:url.pathname,query:url.search,method:options.method||'GET',body})
  if(url.pathname==='/api/pro-settings/automation'){
    fixture.settings={...body.automation,merchant_key_present:!!body.merchant_api_key||fixture.settings.merchant_key_present}
    fixture.pay={...body.gptpay,api_key:''}
    return reply({automation:fixture.settings,gptpay:fixture.pay})
  }
  if(url.pathname==='/api/pro-settings')return reply(fixture.settings)
  if(url.pathname==='/api/gptpay/settings')return reply(fixture.pay)
  if(url.pathname==='/api/gptpay/catalog')return reply({countries:[{code:'US',name:'美国',currency:'USD'}],creditPrices:{pro5x:80,pro20x:100},productAvailability:{pro5x:true,pro20x:true}})
  if(url.pathname==='/api/ejh/settings'||url.pathname==='/api/nexovcc/settings')return reply({enabled:false,templates:[]})
  if(url.pathname==='/api/gptpay/card-cases')return reply([])
  if(url.pathname==='/api/pro-settings/sub2/groups')return reply([])
  if(url.pathname==='/api/pro-accounts'){
    const state=url.searchParams.get('merge_state')||url.searchParams.get('space_state')
    const rows=fixture.accounts.filter(a=>fixture.showAll || (state==='merge_v2'?a.pro_merge_v2.completed_at:state==='in_progress'?!a.pro_merge_v2.completed_at:true))
    return reply({items:rows,total:rows.length,summary:{all:2,in_progress:1,merge_v2:1,merchant:0,deleted:0,merged:0,unmerged:0}})
  }
  if(url.pathname.endsWith('/merge-v2'))return reply(fixture.accounts[0])
  if(url.pathname.endsWith('/merge-v2-restart')) {
    const account=fixture.accounts.find(a=>url.pathname.includes(encodeURIComponent(a.email)))
    account.pro_merge_v2_restart={id:body.request_id,status:'pending'}
    return reply(account)
  }
  if(url.pathname.endsWith('/merge-v2-stage')){fixture.accounts[0].pro_merge_v2.steps[body.stage]=body.status;fixture.accounts[0].pro_merge_v2.accepted_at=now;fixture.accounts[0].pro_merge_v2.status='interrupted';return reply(fixture.accounts[0])}
  throw Error('Unmocked request '+url.pathname)
}
const page=ref('settings'),version=ref(0)
window.mergeV2Fixture.remount=()=>version.value++
createApp({setup:()=>()=>h('div',[
  h('button',{onClick:()=>{page.value='settings';version.value++}},'测试2.0设置'),
  h('button',{onClick:()=>{page.value='accounts';version.value++}},'测试2.0列表'),
  page.value==='settings'?h(ProGPTPayPanel,{key:version.value,mode:'settings',adminAccounts:[]}):h(ProManagementView,{key:version.value}),
])}).mount('#app')
