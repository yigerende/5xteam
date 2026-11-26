import assert from 'node:assert/strict'
import {summarizeUsageCosts} from '../src/ejh-usage-cost.js'
import {financeOverview,financeReport,dailyFinanceCost,prepareFinanceReport} from '../src/ejh-finance.js'
import {accountEarnings} from '../src/ejh-account-profit.js'
import {balanceProfit} from '../src/ejh-profit.js'

const card={id:'c',issuer_linked:true,balance:'7000',balance_at:'2026-11-22T17:32:00+08:00',remote:{orderNo:'opening',status:'2',cardCurrency:'PHP',startTime:'2026-11-20 10:00:00'}}
const op={id:'o',card_id:'c',order_no:'opening',method:'open',status:'completed',created_at:'2026-11-20T10:00:00+08:00',result:{cardAmount:'7000',cardCurrency:'PHP',billAmount:'110.68',billCurrency:'USD'}}
const trade=(id,type,amount,day='2026-11-22')=>({id,card_id:'c',payment_order_id:'case',confirmed:true,confirmed_amount:amount,confirmed_currency:'PHP',trade:{tradeNo:id,authCode:'a',tradeType:type,tradeStatus:['1','2'].includes(type)?'1':'2',cardAmount:amount,cardCurrency:'PHP',billAmount:'0',billCurrency:'USD',createdAt:day+' 17:31:54',tradeTime:'2026-11-20 10:10:00'}})
const auth=trade('auth','1','5794.64','2026-11-20'),ret=trade('ret','2','5787.24')
const row={id:'case',email:'sample@example.test',ejh_card_id:'c',state:'returned',payment_status:'success',plan:'pro5',opened_at:'2026-11-20T10:10:00+08:00',consumption_total:100,charge:{id:'auth',trade_no:'auth',type:'1',status:'1',card_amount:'5794.64',card_currency:'PHP'},returns:[ret]}
const data={cards:[card],operations:[op],cases:[row],trades:[auth,ret],recharge_ledger:{amount:'200'},balance:{data:{abroadList:[{currency:'USD',balance:'100'}]}}}
const calc=(r=row,t=[auth,ret],c=[card],ops=[op])=>summarizeUsageCosts([r],c,ops,t)
let cost=calc()
assert.equal(cost.usd.amount,'0.12');assert.equal(cost.entries[0].amount,'7.40');assert.equal(cost.entries[0].at,Date.parse('2026-11-22T17:31:54+08:00'))
assert.equal(calc({...row,deleted:true}).usd.amount,'0.12','deleted accounts retain historical use')
assert.equal(calc(row,[auth,ret], [{...card,remote:{...card.remote,status:'3'}}]).usd.amount,'0.12','calculation does not wait for closure')
for(const state of ['opened','waiting','failed'])assert.equal(calc({...row,state}).count,0,'not returned: '+state)
for(const r of [{...row,returns:[]},{...row,returns:[{...ret,confirmed:false}]},{...row,returns:[{...ret,trade:{...ret.trade,tradeStatus:'3'}}]}])assert.equal(calc(r).pending,1)
for(const r of [{...row,pro_dead:true},{...row,payment_only:true}])assert.equal(calc(r).count,0,'separate principal accounting')
assert.equal(calc({...row,charge:null}).usd.amount,'0.12','single missing association recovers from issuer evidence')
assert.equal(calc({...row,charge:null},[]).missing,1)
assert.equal(summarizeUsageCosts([], [card], [op], [auth,ret]).usd.amount,'0.12','closed unlinked card is included')
assert.equal(summarizeUsageCosts([], [{...card,remote:{...card.remote,status:'3'}}], [op], [auth,ret]).count,0,'unlinked active card is not assumed complete')
assert.equal(summarizeUsageCosts([row], [card], [op], [auth,ret]).usd.amount,'0.12','linked issuer return never double counts')
assert.equal(summarizeUsageCosts([{...row,state:'waiting'}], [card], [op], [auth,ret]).count,0,'bound unfinished account stays pending even on closed card')
assert.equal(summarizeUsageCosts([], [card], [op], [auth]).count,0,'closure without refund is not evidence')
assert.equal(summarizeUsageCosts([], [card], [op], [auth,{...ret,trade:{...ret.trade,tradeStatus:'3'}}]).count,0)
for(const change of [{confirmed_currency:'USD'},{confirmed_amount:'6000'},{confirmed_amount:'bad'},{card_id:'wrong'},{payment_order_id:'other'}]){
 const v=calc({...row,returns:[{...ret,...change}]});assert.equal(v.missing,1);assert.equal(v.usd.amount,null);assert.equal(v.entries[0].incomplete,true)
}
assert.equal(calc(row,[auth,ret],[],[]).missing,1,'missing opening FX is explicit')
const full=trade('ret','2','5794.64');assert.equal(calc({...row,returns:[full]},[auth,full]).usd.amount,'0.00')
assert.equal(calc({...row,returns:[ret,ret]},[auth,ret,ret]).usd.amount,'0.12','duplicate evidence counts once')
assert.equal(summarizeUsageCosts([row,row],[card],[op],[auth,ret]).usd.amount,'0.12','duplicate case counts once')
assert.equal(summarizeUsageCosts([row,{...row,id:'other'}],[card],[op],[auth,ret]).usd.amount,null,'ambiguous owner is not counted twice')
const refund=trade('ret','5','5787.24')
assert.equal(calc({...row,returns:[refund]},[auth,refund]).usd.amount,'0.12')
const r1=trade('r1','2','3000'),r2=trade('r2','5','2787.24','2026-11-23')
assert.equal(calc({...row,returns:[r1,r2]},[auth,r1,r2]).usd.amount,'0.12')
assert.equal(calc({...row,state:'waiting',returns:[r1]},[auth,r1]).count,0,'partial refund is not settled prematurely')
assert.equal(calc({...row,returns:[r1,r2]},[auth,r1,r2]).entries[0].at,Date.parse('2026-11-23T17:31:54+08:00'))
const noDate={...ret,trade:{...ret.trade,createdAt:'',tradeTime:''}}
assert.equal(calc({...row,returns:[noDate]}).entries[0].at,null,'do not invent receipt time')
const clearing=trade('clear','4','7.40')
assert.equal(calc(row,[auth,ret,clearing]).count,0,'posted capture already pays the full usage cost')
const half={...clearing,trade:{...clearing.trade,cardAmount:'3.70'}}
assert.equal(calc(row,[auth,ret,half]).usd.amount,'0.06','partial capture only offsets its own amount')
assert.equal(calc(row,[auth,ret,{...clearing,trade:{...clearing.trade,tradeStatus:'1'}}]).usd.amount,'0.12','pending capture is not charged twice')
const capture=trade('capture','4','5794.64','2026-11-20')
assert.equal(calc({...row,returns:[refund]},[auth,capture,refund]).count,0,'capture minus refund already includes retained consumption')

const overview=financeOverview(data),before=financeOverview({...data,cases:[{...row,state:'waiting'}]})
assert.equal((Number(overview.earnings.costUSD)-Number(before.earnings.costUSD)).toFixed(2),'0.12')
assert.equal(overview.earnings.usageR,'0.8040');assert.equal(overview.earnings.estimated,true)
const current=accountEarnings(row,[card],[op],[auth,ret]),old=accountEarnings({...row,state:'waiting'},[card],[op],[auth,ret])
assert.equal((Number(old.profitR)-Number(current.profitR)).toFixed(4),'0.8040')
for(const [mode,date,expected] of [['day','2026-11-20','0.00'],['day','2026-11-22','0.12'],['week','2026-11-22','0.12'],['month','2026-11-22','0.12'],['all','2026-11-22','0.12']])assert.equal(financeReport(data,{},mode,date).summary.usageUSD,expected)
assert.equal(dailyFinanceCost(data,'2026-11-22').earnings.costUSD,financeReport(data,{},'day','2026-11-22').summary.earnings.costUSD)
const invalid={...data,cases:[{...row,returns:[{...ret,confirmed_currency:'USD'}]}]}
assert.equal(financeReport(invalid,{},'day','2026-11-22').summary.earnings.costIncomplete,true)
const undated=financeReport({...data,cases:[{...row,returns:[noDate]}]}, {},'day','2026-11-22')
assert.equal(undated.summary.usageUSD,'0.00');assert.ok(undated.undated)
const base={openingUSD:'0.50',reversalUSD:'4.58',clearingUSD:0,consumption:100,usageUSD:'0.12'}
assert.equal(balanceProfit({...base,rechargeUSD:10,refundUSD:1}).profitR,balanceProfit({...base,rechargeUSD:0,refundUSD:1}).profitR,'recharge fee stays excluded')
assert.equal(financeOverview({...data,refund_fees:[{id:'manual',amount:'1',at:'2026-11-22T12:00:00+08:00',note:'test'}]}).earnings.usageR,'0.8040','refund fee and usage remain separate')

// Historical size test: one index pass, no network calls or issuer mutations.
const large={cards:[],operations:[],cases:[],trades:[]}
for(let i=0;i<12000;i++){
 const id='c'+i,order='o'+i,caseID='case'+i
 large.cards.push({...card,id,remote:{...card.remote,orderNo:order}})
 large.operations.push({...op,id:order,card_id:id,order_no:order})
 const a={...auth,card_id:id,id:'auth'+i,trade:{...auth.trade,tradeNo:'auth'+i}},r={...ret,card_id:id,id:'ret'+i,payment_order_id:caseID,trade:{...ret.trade,tradeNo:'ret'+i}}
 large.trades.push(a,r)
 large.cases.push({...row,id:caseID,ejh_card_id:id,charge:{...row.charge,id:a.id,trade_no:a.id},returns:[r]})
}
const started=performance.now(),many=summarizeUsageCosts(large.cases,large.cards,large.operations,large.trades),elapsed=performance.now()-started
assert.equal(many.count,12000);assert.equal(many.usd.amount,'1440.00');assert.ok(elapsed<5000,`usage calculation ${elapsed}ms`)
assert.deepEqual(calc(),cost,'repeated reads do not mutate evidence or append fees')
console.log(`Passed: returned-only usage, original FX, closure/deleted history, invalid/duplicate evidence, partial refunds, clearing deduplication, account profit, period/date parity and 12,000 accounts / 24,000 trades in ${elapsed.toFixed(1)}ms`)
