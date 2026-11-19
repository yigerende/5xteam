import assert from 'node:assert/strict'
import fs from 'node:fs'
import {spawnSync} from 'node:child_process'
import {financeReport} from '../src/ejh-finance.js'
import {createFinanceReportCache} from '../src/ejh-finance-report-client.js'
const card={id:'card',balance:'5',remote:{orderNo:'card',cardNo:'****1234',cardType:'MASTER_B1_5',cardCurrency:'USD',status:'3',startTime:'2026-11-15 10:00:00'}}
const trade=(id,type,amount,day)=>({id,card_id:'card',trade:{tradeNo:id,authCode:'same',cardType:'MASTER_B1_5',tradeType:type,tradeStatus:'2',cardAmount:amount,cardCurrency:'USD',billAmount:amount,billCurrency:'USD',createdAt:day+' 10:00:00',refundBuyPrice:type==='5'?amount:''}})
const data={cards:[card],operations:[],trades:[trade('clear','4','100','2026-11-15'),trade('refund','5','95','2026-11-16')],cases:[{email:'a@example.com',state:'returned',payment_status:'success',plan:'pro5',opened_at:'2026-11-15T23:59:59+08:00',consumption_total:250}],recharge_ledger:{amount:'200'},reversal_fee_percent:6,balance:{data:{abroadList:[{currency:'USD',balance:'100'}]}}}
const history={consumption:[{id:'usage',email:'a@example.com',total:250,start_day:'2026-11-15',end_day:'2026-11-16',daily_checked_at:'2026-11-16T12:00:00+08:00'}],usage_days:[{id:'usage:2026-11-15',day:'2026-11-15',email:'a@example.com',amount:100,source:'sub2_daily'},{id:'usage:2026-11-16',day:'2026-11-16',email:'a@example.com',amount:150,source:'sub2_daily'}]}
const runtime=new URL('../../internal/financecalc/report.mjs',import.meta.url)
for(const [mode,date] of [['day','2026-11-15'],['day','2026-11-16'],['day','2026-11-17'],['week','2026-11-16'],['month','2026-11-16'],['all','2026-11-16']]){
 const child=spawnSync(process.execPath,[runtime.pathname.replace(/^\/(\w:)/,'$1')],{input:JSON.stringify({data,history,mode,date,accounts:true}),encoding:'utf8'})
 assert.equal(child.status,0,child.stderr)
 const actual=JSON.parse(child.stdout),expected=JSON.parse(JSON.stringify(financeReport(data,history,mode,date)))
 for(const key of Object.keys(expected).filter(k=>k!=='overview'))assert.deepEqual(actual[key],expected[key],mode+':'+date+':'+key)
 for(const key of Object.keys(actual.overview))assert.deepEqual(actual.overview[key],expected.overview[key],key)
}
let clock=0,calls=0,resolve
const cache=createFinanceReportCache(async()=>{calls++;return {summary:calls}},()=>clock),params={mode:'day',date:'2026-11-19'}
assert.equal(cache.peek(params),null)
await cache.read(params);assert.equal(cache.peek(params).summary,1)
clock=300001;assert.equal(cache.peek(params),null)
cache.clear();assert.equal(cache.peek(params),null)
const pendingCache=createFinanceReportCache(()=>new Promise(ok=>resolve=ok));const pending=pendingCache.read(params)
pendingCache.clear();resolve({secret:'old login'});await pending;assert.equal(pendingCache.peek(params),null)
for(let i=0;i<30;i++)await cache.read({...params,date:'date'+i})
assert.equal(cache.peek({...params,date:'date0'}),null)
assert.ok(cache.peek({...params,date:'date29'}))
const view=fs.readFileSync(new URL('../src/components/EJHFinanceView.vue',import.meta.url),'utf8')
assert.ok(!view.includes('/api/ejh/overview')&&!view.includes('/api/ejh/finance?snapshots'))
console.log('Passed: shared server formula parity, cross-day refunds, original-cost attribution, all periods, detail fields, bounded session cache, expiry and login isolation')
