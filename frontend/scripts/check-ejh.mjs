import { refreshEJHSync } from '../src/ejhSync.js'
import { applyRefundFeeOverrides } from '../src/ejh-refund-cost.js'
import { applyPaymentPolicies } from '../src/payment-only.js'
import {cardOrigin} from '../src/card-platform.js'
import { isClosedCard } from '../src/card-status.js'
import { createEJHRefreshGate } from '../src/ejhRefresh.js'
import { createLatestRequest } from '../src/latestRequest.js'
import assert from 'node:assert/strict'
import { newestCards, cancelCardReason, runEJHBatch, refreshCancellationProgress } from '../src/ejh.js'
import { currencies, currencyOptions, searchCurrencyOptions } from '../src/ejh-currencies.js'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { returnTypes, operationStates, caseSummary, summarizeCardBalances, returnStatus, returnStatusLabels, cardHasCase, cardMatchesSearch, cardStates, cardState, cardMatchesFilters, readCardView, saveCardView, ejhCardEntry } from '../src/ejh.js'
const source=readFileSync(new URL('../src/components/EJHManagementView.vue',import.meta.url),'utf8')
const script=source.split('<script setup>')[1].split('</script>')[0]
const parsed=ts.createSourceFile('ejh.js',script,ts.ScriptTarget.Latest,true,ts.ScriptKind.JS)
const methods=parsed.statements.filter(s=>ts.isFunctionDeclaration(s)&&['action','show','cardAction','readCard','openCards','cancelSelected'].includes(s.name?.text)).map(s=>s.getText(parsed)).join('\n')
function fixture(result){
 const calls=[],message={text:'',type:''},openForm={count:3,request_id:'',input:{cardAmount:'20',cardCurrency:'USD'}},detail={value:null}
 const ctx=vm.createContext({refreshEJHSync,refreshSaved(){},cardReading:{},refreshing:{value:false},refreshProgress:{value:null},initializing:{value:false},initialError:{value:''},settingsRequest:createLatestRequest(),dataRequest:createLatestRequest(),applyRefundFeeOverrides,detail:{value:null},applyPaymentPolicies,cardOrigin,savedEnabled:{value:false},savedRefreshSeconds:{value:30},isClosedCard,cardState,ejhRefreshGate:createEJHRefreshGate({storage:()=>null}),setInterval,clearInterval,progress:{value:null},selectedCards:{value:[]},openUncertain:{value:false},page:{value:1},disposed:false,runEJHBatch,refreshCancellationProgress,cancelReason:()=>String(),message,openForm,detail,busy:{value:false},operationStates,window:{confirm:()=>true,prompt:()=> '20'},crypto:{randomUUID:()=> 'fixture-id'},reloadCard:async id=>ctx.data.value.cards.find(c=>c.id===id),load:async()=>{},api:async(path,options)=>{calls.push({path,options});return result}})
 vm.runInContext(methods+'\nthis.methods={cardAction,openCards,cancelSelected}',ctx)
 return {ctx,calls,message,openForm,detail}
}
const card={id:'fixture',remote:{cardCurrency:'USD'}}
for(const action of ['refresh','balance']){const f=fixture({status:'1'});await f.ctx.methods.cardAction(card,action);assert.equal(f.message.type,'success','Issuer status is not an operation failure')}
{const f=fixture({status:'unknown',error:'fixture unknown'});await f.ctx.methods.cardAction(card,'increase');assert.equal(f.message.type,'error');assert.equal(f.calls.length,1)}
{const f=fixture({status:'unknown'});await f.ctx.methods.openCards();assert.equal(f.calls.length,1,'Batch must stop at unknown result');assert.equal(f.openForm.request_id,'fixture-id','Keep request ID after uncertainty')}
{const f=fixture({status:'completed'});await f.ctx.methods.openCards();assert.equal(f.calls.length,3);assert.equal(f.openForm.request_id,'')}
{const f=fixture({cardNo:'fixture'});await f.ctx.methods.cardAction(card,'details');assert.equal(f.detail.value.data.cardNo,'fixture')}
{const f=fixture({status:'processing'});await f.ctx.methods.cardAction(card,'cancel');assert.equal(f.message.type,'info');assert.equal(f.ctx.progress.value.waitingForIssuer,true);assert.match(f.message.text,/处理中/);assert.equal(f.calls.length,1)}
{const f=fixture({status:'unknown',error:'EJH 300002'});await f.ctx.methods.cardAction(card,'cancel');assert.equal(f.message.type,'info');assert.equal(f.ctx.progress.value.unknown,1);assert.match(f.message.text,/暂未确认/);assert.equal(f.calls.length,1)}
assert.deepEqual([...returnTypes],['2','5','16'])
assert.ok(caseSummary({email:'fixture@example.com',state:'returned'}).includes('已回款'))
assert.ok(!returnTypes.has('17'),'Card closure is not an account refund')
console.log('Passed: EJH refresh, balance, detail, unknown-result stop, batch idempotency and return labels')

const snapshot = (balance, currency='USD', extra={}) => ({balance,balance_at:'2026-10-14T00:00:00Z',remote:{cardCurrency:currency},...extra})
const summary = summarizeCardBalances([snapshot('0.1'),snapshot('0.2'),snapshot('0'),snapshot('-0.05'),snapshot('20','EUR'),snapshot('7','USD',{balance_error:'timeout'}),snapshot('', 'USD', {balance_at:null}),snapshot('bad'),snapshot('10','???')])
assert.deepEqual(summary.totals,[{currency:'EUR',count:1,amount:'20.00'},{currency:'USD',count:5,amount:'7.25'}])
assert.equal(summary.missing,3);assert.equal(summary.failed,1);assert.equal(summary.known,6)
assert.equal(summarizeCardBalances([snapshot('999999999999.999999'),snapshot('0.000001')]).totals[0].amount,'1000000000000.00')
assert.deepEqual(summarizeCardBalances([]).totals,[])
const balanceMethods=parsed.statements.filter(s=>ts.isFunctionDeclaration(s)&&['action','show','refreshAllBalances','historicalBalance'].includes(s.name?.text)).map(s=>s.getText(parsed)).join('\n')
for(const stop of [false,true]){
 const calls=[],allCards=[{id:'one',remote:{orderNo:'one'}},{id:'two',remote:{orderNo:'two'}},{id:'closed',remote:{status:'2',orderNo:'closed'}},{id:'closing',remote:{status:'8',orderNo:'closing'}}]
 const ctx=vm.createContext({refreshEJHSync,refreshSaved(){},cardReading:{},refreshing:{value:false},refreshProgress:{value:null},initializing:{value:false},initialError:{value:''},settingsRequest:createLatestRequest(),dataRequest:createLatestRequest(),applyRefundFeeOverrides,detail:{value:null},applyPaymentPolicies,cardOrigin,savedEnabled:{value:false},savedRefreshSeconds:{value:30},isClosedCard,cardState,ejhRefreshGate:createEJHRefreshGate({storage:()=>null}),setInterval,clearInterval,progress:{value:null},selectedCards:{value:[]},openUncertain:{value:false},page:{value:1},disposed:false,runEJHBatch,refreshCancellationProgress,cancelReason:()=>String(),message:{text:'',type:''},busy:{value:false},disposed:false,data:{value:{cards:allCards}},balanceProgress:{value:null},balanceFailures:{value:[]},api:async(path,options)=>{calls.push({path,options});if(path==='/api/ejh/view?section=balance_targets')return {cards:allCards.filter(c=>!['2','8'].includes(c.remote.status))};if(path.includes('/one/')){if(stop)ctx.disposed=true;throw Error('fixture card failure')}return {}}})
 vm.runInContext(balanceMethods+'\nthis.run=refreshAllBalances',ctx)
 await ctx.run()
 assert.equal(calls.filter(c=>c.path==='/api/ejh/balance').length,1)
 assert.equal(calls.filter(c=>c.options?.body?.action==='balance').length,stop?1:2,'Refresh must include all cards, continue on failure, stop when leaving')
 assert.equal(calls.filter(c=>c.path.includes('/closed/')).length,0,'closed cards must not refresh');
 assert.equal(calls.filter(c=>c.path.includes('/closing/')).length,0,'closing cards must not query balance');
 assert.equal(ctx.balanceProgress.value,null);assert.equal(ctx.balanceFailures.value.length,1)
}
console.log('Passed: precise multi-currency balances, negative/zero/missing values, cached failure fallback, manual-only refresh, all-card iteration and navigation cancellation')

const cases=[{card_id:'alias',ejh_card_id:'canonical',email:'User@example.com'},{card_id:'other',email:'user@example.com'}]
assert.equal(cardHasCase({id:'canonical'},cases[0]),true)
assert.equal(cardMatchesSearch({id:'canonical'},cases,' USER@EXAMPLE.COM '),true)
assert.equal(cardMatchesSearch({id:'other'},cases,'user@example.com','canonical'),false)
assert.equal(cardMatchesSearch({id:'unrelated'},cases,'user@example.com'),false)
assert.equal(cardMatchesSearch({id:'canonical',remote:{cardNo:'****4242'}},cases,'4242'),true)
assert.equal(ejhCardEntry({email:'a',activation_card:{card_name:'EJH',card_last4:'4242'}}),null)
assert.deepEqual(ejhCardEntry({email:'a',activation_card:{ejh_card_id:'canonical'}}),{email:'a',cardID:'canonical'})
const storage={values:new Map(),getItem(k){return this.values.get(k)},setItem(k,v){this.values.set(k,v)}}
assert.equal(readCardView(storage),'cards')
saveCardView(storage,'list');assert.equal(readCardView(storage),'list')
saveCardView(storage,'cards');assert.equal(readCardView(storage),'cards')
saveCardView(storage,'invalid');assert.equal(readCardView(storage),'cards')
const blocked={getItem(){throw Error('blocked')},setItem(){throw Error('blocked')}}
assert.equal(readCardView(blocked),'cards');saveCardView(blocked,'list')
assert.equal(returnStatus('bogus'),'unknown')
for(const state of ['unopened','opened','waiting','returned','unverified','unmatched'])assert.ok(returnStatusLabels[returnStatus(state)])
for(const [file,component] of [['ProFinancialCell.vue','ReturnStatusBadge'],['ProGPTPayPanel.vue','ReturnCaseLine'],['EJHManagementView.vue','ReturnStatusBadge']]) {
 const view=readFileSync(new URL('../src/components/'+file,import.meta.url),'utf8')
 assert.ok(view.includes('<'+component),'Shared colors missing in '+file)
}
assert.ok(source.includes("cardView==='cards'"));assert.ok(source.includes('class="ejh-card-table"'))
console.log('Passed: canonical/alias card lookup, account-email search, exact navigation, non-EJH exclusion, persistent view selection and shared status badges')
const templateSection=source.match(/<section[^>]*data-settings-tab="templates"[^>]*>([\s\S]*?)<\/section>/)?.[1]
assert.ok(templateSection,'Template settings section is present')
const templateLabels=[...templateSection.matchAll(/<label\b[^>]*>([\s\S]*?)<\/label>/g)].map(match=>match[1])
const requiredTemplateFields=['name','cardType','cardAmount','cardCurrency','billCurrency','valid_days','useTime','supportedMccGroup']
for(const field of requiredTemplateFields){
 const label=templateLabels.find(value=>value.includes('="t.'+field+'"'))
 assert.ok(label,field+' has a field label')
 assert.ok(label.includes('class="ejh-required" aria-hidden="true">*</span>'),field+' has a visible required marker')
 assert.match(label,/<(?:input|select|EJHCurrencySelect)\b[^>]*\srequired(?:\s|\/?>)/,field+' has native required semantics')
}
for(const field of ['companyRate','extInfoyx','authCount']){
 const label=templateLabels.find(value=>value.includes('="t.'+field+'"'))
 assert.ok(label,field+' is present')
 assert.ok(!label.includes('ejh-required'),field+' remains optional')
}
assert.ok(!templateLabels.find(value=>value.includes('v-model="t[key]"')).includes('ejh-required'),'Optional order parameters remain optional')
assert.equal(templateLabels.filter(value=>value.includes('ejh-required')).length,8)
assert.ok(templateSection.includes('为必填项'))
assert.ok(source.includes('.ejh-required{color:var(--red);'),'Required markers follow the theme error color')
console.log('Passed: eight required template markers, native required semantics and optional fields remain optional')

// Currency dictionary is the supplier's published 157-code set, not generic ISO.
const expectedCodes = 'AED AFN ALL AMD ANG AOA ARS AUD AWG AZN BAM BBD BDT BGN BHD BIF BMD BND BOB BRL BSD BTN BWP BYN BZD CAD CDF CHF CLP CNH CNY COP CRC CUP CVE CZK DJF DKK DOP DZD EGP ETB EUR FJD FKP GBP GEL GHS GIP GMD GNF GTQ GYD HKD HNL HRK HTG HUF IDR ILS INR IQD IRR ISK JMD JOD JPY KES KGS KHR KMF KPW KRW KWD KYD KZT LAK LBP LKR LRD LSL LYD MAD MDL MGA MKD MMK MNT MOP MRU MUR MVR MWK MXN MYR MZN NAD NGN NIO NOK NPR NZD OMR PAB PEN PGK PHP PKR PLN PYG QAR RON RSD RUB RWF SAR SBD SCR SDG SEK SGD SHP SLL SOS SRD SSP STD STN SVC SYP SZL THB TJS TMT TND TOP TRY TTD TWD TZS UAH UGX USD UYU UZS VES VND VUV WST XAF XCD XOF XPF YER ZAR ZMW ZWL'.split(' ')
assert.deepEqual(currencies.map(item=>item.code), expectedCodes)
assert.equal(new Set(currencies.map(item=>item.code)).size,157)
for(const item of currencies) { assert.ok(item.name);assert.ok(item.region) }
for(const code of ['CNH','ANG','HRK','SLL','STD','ZWL']) assert.ok(currencies.some(item=>item.code===code))
assert.equal(currencyOptions().length,157)
assert.equal(currencyOptions('USD').filter(item=>item.value==='USD').length,1)
assert.equal(currencyOptions('ZZZ')[0].value,'ZZZ')
assert.ok(currencyOptions('ZZZ')[0].label.includes('原值'))
assert.ok(currencyOptions('CNH').find(item=>item.value==='CNH').label.includes('离岸人民币'))
assert.equal(currencyOptions().length,157,'A legacy value must not mutate the official dictionary')
for(const field of ['t.cardCurrency','t.billCurrency']) {
 assert.ok(source.includes('v-model="'+field+'"'))
 assert.ok(!source.includes('<input v-model="'+field+'"'),'Currency must not accept arbitrary typing')
}
const currencyComponent=readFileSync(new URL('../src/components/EJHCurrencySelect.vue',import.meta.url),'utf8')
assert.ok(currencyComponent.includes('<select v-bind="$attrs" :aria-label="label" :value="modelValue"'))
assert.ok(currencyComponent.includes("emit('update:modelValue', event.target.value)"))
assert.ok(currencyComponent.includes('<option value="">请选择币种</option>'))
assert.ok(currencyComponent.includes(':value="option.value"'),'Submit codes, not localized labels')
assert.equal(searchCurrencyOptions('', ' usd ').options[0].value, 'USD')
assert.ok(searchCurrencyOptions('', '人民币').options.some(v=>v.value==='CNY'))
assert.ok(searchCurrencyOptions('', '日本').options.some(v=>v.value==='JPY'))
assert.equal(searchCurrencyOptions('EUR', 'usd').options[0].value, 'EUR', 'Search preserves the selected value')
assert.equal(searchCurrencyOptions('EUR', 'not-a-currency').matchCount, 0)
assert.equal(searchCurrencyOptions('ZZZ', 'usd').options[0].value, 'ZZZ', 'Legacy selection is preserved')
assert.equal(searchCurrencyOptions('', '').options.length, 157)
assert.ok(currencyComponent.includes('defineOptions({ inheritAttrs: false })'), 'Required and disabled attributes must reach the select')
console.log('Passed: searchable currencies, code/name/region matching, legacy and selected-value preservation, required-select forwarding')
const currencyMethods=parsed.statements.filter(s=>ts.isFunctionDeclaration(s)&&['save','action','show','chooseTemplate','cardStatus'].includes(s.name?.text)).map(s=>s.getText(parsed)).join('\n')
const settings={templates:[{id:'currency-template',name:'币种测试',valid_days:30,cardType:'fixture',cardAmount:'20',cardCurrency:'CNH',billCurrency:'EUR'}]}
const calls=[]
const currencyContext=vm.createContext({refreshEJHSync,refreshSaved(){},cardReading:{},refreshing:{value:false},refreshProgress:{value:null},initializing:{value:false},initialError:{value:''},settingsRequest:createLatestRequest(),dataRequest:createLatestRequest(),applyRefundFeeOverrides,detail:{value:null},applyPaymentPolicies,cardOrigin,savedEnabled:{value:false},savedRefreshSeconds:{value:30},isClosedCard,cardState,ejhRefreshGate:createEJHRefreshGate({storage:()=>null}),setInterval,clearInterval,progress:{value:null},selectedCards:{value:[]},openUncertain:{value:false},page:{value:1},disposed:false,runEJHBatch,refreshCancellationProgress,cancelReason:()=>String(),cardStates,cardState,settings,data:{value:{}},openForm:{template:'currency-template',input:null},busy:{value:false},message:{},api:async(path,options)=>{const body=JSON.parse(JSON.stringify(options.body));calls.push({path,body});return body}})
vm.runInContext(currencyMethods+'\nthis.run={save,chooseTemplate,cardStatus}',currencyContext)
await currencyContext.run.save()
assert.equal(calls[0].path,'/api/ejh/settings')
assert.equal(calls[0].body.templates[0].cardCurrency,'CNH')
assert.equal(calls[0].body.templates[0].billCurrency,'EUR')
currencyContext.run.chooseTemplate()
assert.equal(currencyContext.openForm.input.cardCurrency,'CNH')
assert.equal(currencyContext.openForm.input.billCurrency,'EUR')
for(const status of ['1','2','3','4','5','6','7','8']) assert.notEqual(currencyContext.run.cardStatus({remote:{status}}),'状态待核实')
assert.equal(currencyContext.run.cardStatus({remote:{status:'unknown'}}),'状态待核实')
console.log('Passed: all 157 official currencies, historical-value preservation, dropdown bindings, save/issue payload codes and eight card statuses')

assert.ok(source.includes('v-model="settings.single_active_account"'))
for(const enabled of [false,true]) {
 settings.single_active_account=enabled
 await currencyContext.run.save()
 assert.equal(calls.at(-1).body.single_active_account,enabled)
}
assert.ok(source.includes('card.issuer_linked!==false'))
const unlinked=fixture({});await unlinked.ctx.methods.cardAction({issuer_linked:false},'increase');assert.equal(unlinked.calls.length,0)
const cardViewSource=readFileSync(new URL('../src/components/ProGPTPayPanel.vue',import.meta.url),'utf8')
assert.ok(cardViewSource.includes('<th>类型</th>'))
assert.ok(cardViewSource.includes('v-model="editor.card_type"'))
assert.ok(cardViewSource.includes("card_type: card?.card_type || 'other'"))
assert.ok(cardViewSource.includes('card_type: card.card_type'))
assert.ok(cardViewSource.includes('/api/gptpay/card-cases'))
console.log('Passed: EJH switch false/true payloads, card type column/default/selector, separate associations and unlinked-card action guard')

// Entry, cancellation, failed-only refresh and concurrent navigation are exercised
// through the complete component in check-ejh-page.mjs.
const sorted=newestCards([{id:'old',local:{created_at:'2026-10-01'},synced_at:'2026-10-20'},{id:'new',local:{created_at:'2026-10-15'}},{id:'missing'}])
assert.deepEqual(sorted.map(c=>c.id),['new','old','missing'])
const usable={id:'card',remote:{status:'1',orderNo:'order'},local:{active_accounts:0,pending_accounts:0}}
assert.equal(cancelCardReason(usable), '')
assert.equal(cancelCardReason({...usable,local:{active_accounts:3}},[],[{card_id:'card',state:'returned'},{ejh_card_id:'card',state:'unopened'}]),'','Return states override historical occupancy')
for(const state of ['opened','waiting','unknown'])assert.ok(cancelCardReason(usable,[],[{card_id:'card',state:'returned'},{ejh_card_id:'card',state}]))
assert.equal(cancelCardReason(usable,[],[{card_id:'other',state:'waiting'}]),'')
for(const card of [{...usable,issuer_linked:false},{...usable,remote:{status:'2'}},{...usable,remote:{status:'8'}},{...usable,error:'review'}])assert.ok(cancelCardReason(card))
assert.ok(cancelCardReason(usable,[{card_id:'card',status:'unknown'}]))
assert.ok(cancelCardReason(usable,[{order_no:'order',status:'submitting'}]))
assert.ok(cancelCardReason(usable,[{order_no:'order',status:'processing'}]))
assert.equal(cardState({remote:{status:'8',isLocked:'1'},error:'stale detail error'}),'8')
assert.equal(cardState({remote:{status:'2',isLocked:'1'}}),'2')
assert.equal(cardState({...usable,local:{issuer_cancel_status:'processing',issuer_locked:true}}),'8')
assert.equal(cardState({...usable,local:{issuer_cancel_status:'unknown',issuer_locked:true}}),'cancel_pending')
{
 const p={cancellation:true,startedAt:Date.now(),done:0,success:0,failed:0,unknown:0,results:[]},calls=[]
 await runEJHBatch([1,2].map(i=>({method:'cancel',label:'card '+i,request_id:'cancel-'+i})),p,async item=>{calls.push(item.request_id);return {status:'processing'}})
 refreshCancellationProgress(p)
 assert.equal(calls.length,2,'Accepted pending cancellation must not stop unrelated selected cards')
 assert.equal(p.processing,2);assert.equal(p.success,0);assert.equal(p.waitingForIssuer,true)
 refreshCancellationProgress(p,[{id:'cancel-1',method:'cancel',status:'completed'},{id:'cancel-2',method:'cancel',status:'processing',check_error:'timeout'}])
 assert.equal(p.success,1);assert.equal(p.processing,1);assert.match(p.phase,/后台将重试/)
 refreshCancellationProgress(p,[{id:'cancel-1',method:'cancel',status:'completed'},{id:'cancel-2',method:'cancel',status:'completed'}])
 assert.equal(p.success,2);assert.equal(p.processing,0);assert.equal(p.waitingForIssuer,false)
 const endedAt=p.cancellationFinishedAt, elapsed=p.elapsed, now=Date.now
 try{Date.now=()=>endedAt+60000;refreshCancellationProgress(p);assert.equal(p.elapsed,elapsed,'Completed cancellation duration must stop increasing')}finally{Date.now=now}
 assert.equal(calls.length,2,'Progress refresh never resubmits')
}
for(const status of ['completed','unknown','failed','throw','stop']) {
 const p={done:0,success:0,failed:0,unknown:0,results:[]},calls=[]
 await runEJHBatch([1,2,3].map(i=>({label:'card '+i,request_id:'request-'+i})),p,async item=>{
  calls.push(item.request_id);if(status==='throw')throw Error('connection lost')
  if(status==='stop')p.stopRequested=true
  return {status:status==='stop'?'completed':status}
 })
 assert.equal(calls.length,status==='completed'?3:1)
 assert.equal(p.done,calls.length);assert.equal(p.results[0].request_id,'request-1')
 assert.equal(p.unknown,status==='unknown'||status==='throw'?1:0)
 assert.equal(p.failed,status==='failed'?1:0)
}
{
 const f=fixture({status:'completed'});let resolveFirst
 const calls=[]
 f.ctx.api=async(path,options)=>{calls.push(JSON.parse(JSON.stringify(options.body)));if(calls.length===1)await new Promise(r=>resolveFirst=r);return {status:'completed'}}
 const running=f.ctx.methods.openCards()
 assert.equal(f.ctx.busy.value,true);assert.equal(f.ctx.progress.value.total,3);assert.match(f.ctx.progress.value.phase,/1 \/ 3/)
 f.openForm.count=1;f.openForm.input.cardAmount='999';resolveFirst();await running
 assert.equal(calls.length,3);assert.ok(calls.every(c=>c.input.cardAmount==='20'),'Freeze batch quantity and payload before awaiting supplier')
}
{
 const f=fixture({status:'unknown'});await f.ctx.methods.openCards();await f.ctx.methods.openCards()
 assert.equal(f.calls.length,1,'Uncertain opening cannot start another batch')
}
{
 const f=fixture({status:'completed'});f.ctx.disposed=true;await f.ctx.methods.openCards();assert.equal(f.calls.length,0)
}
for(const status of ['completed','unknown']) {
 const f=fixture({status});f.ctx.selectedCards.value=['one','two'];f.ctx.data={value:{cards:['one','two'].map(id=>({...usable,id}))}}
 await f.ctx.methods.cancelSelected()
 assert.equal(f.calls.length,status==='completed'?2:1);assert.ok(f.calls.every(c=>c.options.body.action==='cancel'&&c.options.body.confirm))
 assert.equal(f.ctx.progress.value.success,status==='completed'?2:0)
}
console.log('Passed: newest-first order, cancellation selection guards, progress, frozen opening payload, uncertainty stop, disposal and batch cancellation')
import './check-ejh-money-trades.mjs'
