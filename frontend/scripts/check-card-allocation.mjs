import { applyPaymentPolicies } from '../src/payment-only.js'
import {cardOrigin} from '../src/card-platform.js'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { computed, reactive, ref } from 'vue'

for (const file of ['ProRechargeDialog.vue', 'ProAutoDialog.vue']) {
 const source = readFileSync(new URL('../src/components/' + file, import.meta.url), 'utf8')
 const parsed = ts.createSourceFile(file, source.split('<script setup>')[1].split('</script>')[0], ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
 const methods = parsed.statements.filter(s => ts.isFunctionDeclaration(s) && ['loadCards','open','start','submit','changeProvider'].includes(s.name?.text)).map(s => s.getText(parsed)).join('\n')
 const providerState = parsed.statements.filter(s => ts.isVariableStatement(s) && s.declarationList.declarations.some(d => ['autoCardEnabled','autoCardLabel'].includes(d.name.getText(parsed)))).map(s => s.getText(parsed)).join('\n')
 for (const provider of ['ejh','nexovcc']) for (const auto of [false,true]) for (const available of [false,true]) {
  const calls=[]
  const settings=file==='ProAutoDialog.vue'?ref({}):reactive({})
  const ctx=vm.createContext({applyPaymentPolicies,cardOrigin,computed,props:{defaultPageSize:50},blockedCards:ref([]),occupancyMessage:ref(''),settings,cardID:ref(''),cards:ref([]),total:ref(0),page:ref(1),pageSize:ref(10),busy:ref(false),preparingCard:ref(false),canStart:ref(true),email:ref(''),order:ref(null),error:ref(''),opened:ref(false),openedAccount:ref(null),state:ref({}),requestID:'',timer:0,generation:0,clearTimeout(){},schedule(){},emit(){},path:()=>'/auto-pro',payRequestID:()=> 'local-request',api:async(path,options)=>{
   calls.push({path,options})
   if(options) return {pro_auto:{status:'running'}}
   if(path==='/api/pro-settings')return {card_provider:provider,ejh_auto_open:provider==='ejh'?auto:!auto,nexovcc_auto_open:provider==='nexovcc'?auto:!auto}
   if(path==='/api/gptpay/settings')return {provider:'tokenseek',plan_code:'pro5',key_present:true}
   if(path.startsWith('/api/gptpay/cards')){
    assert.equal(new URL(path,'http://local').searchParams.get('card_provider'),file==='ProAutoDialog.vue'?settings.value.card_provider:'configured',file+' must filter the issuer before pagination')
    return {items:available?[{id:'existing'}]:[],total:available?1:0}
   }
   if(path.startsWith('/api/gptpay/orders'))return {items:[]}
   return {}
  }})
  vm.runInContext(providerState+'\n'+methods+'\nthis.run={open,loadCards,autoCardLabel,'+(file==='ProAutoDialog.vue'?'start,changeProvider':'submit')+'}',ctx)
  await ctx.run.open({email:'local@example.com'})
  assert.equal(ctx.run.autoCardLabel.value,provider==='nexovcc'?'环球':'EJH')
  assert.equal(ctx.cardID.value,auto?'':available?'existing':'')
  assert.ok(calls.some(c=>c.path==='/api/pro-settings'))
  await (ctx.run.start||ctx.run.submit)(false)
  assert.equal(calls.find(c=>c.options)?.options.body.card_id,auto?'':available?'existing':'')
  await ctx.run.loadCards()
  assert.equal(ctx.cardID.value,auto?'':available?'existing':'')
  if(file==='ProAutoDialog.vue') {
   assert.equal(calls.find(c=>c.options)?.options.body.card_provider,provider)
   const other=provider==='ejh'?'nexovcc':'ejh'
   settings.value.card_provider=other
   await ctx.run.changeProvider()
   assert.equal(ctx.page.value,1)
   assert.equal(ctx.run.autoCardLabel.value,other==='nexovcc'?'环球':'EJH')
   assert.equal(ctx.cardID.value,!auto?'':available?'existing':'')
   await ctx.run.start()
   assert.equal(calls.filter(c=>c.options).at(-1).options.body.card_provider,other)
   assert.equal(calls.filter(c=>c.path==='/api/pro-settings'&&c.options).length,0,'manual choice must not overwrite defaults')
  }
 }
 assert.ok(source.includes('无可用卡时从 ${autoCardLabel} 补卡'))
 if(file==='ProAutoDialog.vue') {
  const names=['replacementFailed','active','deletionSubmitted','resumable','retryCurrent','restartable','canStart']
  const state=ref({restock_retry_available:true,steps:{}})
  const ctx=vm.createContext({applyPaymentPolicies,cardOrigin,computed,state,quotaResumeAvailable:ref(false)})
  const flags=parsed.statements.filter(s=>ts.isVariableStatement(s)&&s.declarationList.declarations.some(d=>names.includes(d.name.getText(parsed)))).map(s=>s.getText(parsed)).join('\n')
  vm.runInContext(flags+'\nthis.flags={retryCurrent,restartable,canStart}',ctx)
  assert.equal(ctx.flags.retryCurrent.value,true,'pre-workflow failure needs explicit retry')
  assert.equal(ctx.flags.restartable.value,true)
  assert.equal(ctx.flags.canStart.value,false,'claimed account must not appear as a new start')
  state.value.status='running'
  assert.equal(ctx.flags.retryCurrent.value,false)
  state.value={steps:{}}
  assert.equal(ctx.flags.retryCurrent.value,false)
  assert.equal(ctx.flags.canStart.value,true)
 }
}
const ejh=readFileSync(new URL('../src/components/EJHManagementView.vue',import.meta.url),'utf8')
for(const [code,label] of [['ecom','跨境电商'],['flight','机票'],['hotel','酒店'],['trv','非酒店机票']]) assert.ok(ejh.includes('<option value="'+code+'">'+code+' · '+label+'</option>'))
console.log('Passed: EJH/Nexovcc manual/auto selection, independent auto-open settings, pagination, request card IDs and official MCC labels')
