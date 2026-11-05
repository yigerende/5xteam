import assert from 'node:assert/strict'
import { applyEJHOverviewResponse } from '../src/ejh-overview-state.js'
import { calculateEJHOverview } from '../src/ejh-overview.js'
const source={cards:[],cases:[],operations:[],trades:[],consumption_history:[],recharge_ledger:{amount:'100'},balance:{data:{abroadList:[{currency:'USD',balance:'99'}]}}}
let full=applyEJHOverviewResponse(null,{mode:'full',revision:'r1',source_revision:'d1',server_time:'now',data:source})
assert.deepEqual(calculateEJHOverview(full,0),calculateEJHOverview(source,0))
const after={...source,consumption_history:[{id:'a',email:'a@example.com',total:10},{id:'b',email:'b@example.com',total:20}],balance:{data:{abroadList:[{currency:'USD',balance:'98'}]}}}
const delta={mode:'delta',base_revision:'r1',revision:'r2',source_revision:'d2',changes:{consumption_history:{rows:{upsert:after.consumption_history,removed:[],order:['a','b']}},balance:{value:after.balance}},removed:[]}
const next=applyEJHOverviewResponse(full,delta)
assert.deepEqual(calculateEJHOverview(next,0),calculateEJHOverview(after,0),'incremental inputs changed accounting')
assert.equal(full.balance.data.abroadList[0].balance,'99','old snapshot mutated')
const gone=applyEJHOverviewResponse(next,{...delta,base_revision:'r2',revision:'r3',changes:{consumption_history:{rows:{removed:['a'],upsert:[{id:'b',email:'b@example.com',total:30}],order:['b']}}},removed:['balance']})
assert.equal(gone.consumption_history.length,1);assert.equal(gone.consumption_history[0].total,30);assert.ok(!Object.hasOwn(gone,'balance'))
assert.throws(()=>applyEJHOverviewResponse(full,{...delta,base_revision:'other'}),/版本/)
assert.throws(()=>applyEJHOverviewResponse(full,{...delta,changes:{cards:{rows:{removed:[],upsert:[],order:['missing']}}}}),/不完整/)
assert.throws(()=>applyEJHOverviewResponse(null,{mode:'unchanged',revision:'r1'}),/版本/)
assert.equal(applyEJHOverviewResponse(next,{mode:'unchanged',revision:'r2',source_revision:'d3'}).source_revision,'d3')

// Refunds, closed cards, payment-only spend and dead accounts must retain all
// their historical evidence when a later update changes only a few rows.
const at='2026-11-05T05:00:00Z',now=Date.parse(at)
const card=id=>({id,issuer_linked:true,balance:'40.00',balance_at:at,remote:{orderNo:id,status:'1',cardCurrency:'USD'}})
const trade=(id,card_id,type,amount)=>({id,card_id,trade:{tradeNo:id,authCode:card_id,tradeType:type,tradeStatus:type==='1'?'1':'2',cardAmount:amount,cardCurrency:'USD',billAmount:amount,billCurrency:'USD',tradeTime:at}})
const live=card('live'),closed={...card('closed'),remote:{...card('closed').remote,status:'2'}},dead=card('dead'),external=card('external')
const cards=[live,closed,dead,external]
const operations=cards.map(c=>({id:'open-'+c.id,card_id:c.id,order_no:c.id,method:'open',status:'completed',created_at:at,result:{cardAmount:'100',cardCurrency:'USD',billAmount:'100',billCurrency:'USD'}}))
const waiting={id:'waiting',ejh_card_id:'live',state:'waiting',consumption_total:40,charge:{type:'1',status:'1',trade_no:'hold',card_amount:'60',card_currency:'USD'}}
const sample={cards,operations,trades:[trade('hold','live','1','60'),trade('clear','live','4','2'),trade('closed-refund','closed','5','100'),trade('dead-hold','dead','1','60'),trade('external-hold','external','1','60')],cases:[waiting,{...waiting,id:'old-returned',ejh_card_id:'closed',state:'returned',consumption_total:20},{...waiting,id:'dead-case',ejh_card_id:'dead',pro_dead:{at},charge:{...waiting.charge,trade_no:'dead-hold'}},{...waiting,id:'external-case',ejh_card_id:'external',charge:{...waiting.charge,trade_no:'external-hold'}}],refund_fees:[{id:'fee',amount:'3.00',at,revision:1}],payment_policies:[{card_id:'external',mode:'payment_only'}],consumption_history:[{id:'deleted-history',email:'deleted@example.com',total:90}],reversal_fee_percent:6,recharge_ledger:{amount:'1000'},balance:{at,data:{abroadList:[{currency:'USD',balance:'600'}]}},sync:{last_success:at}}
const initial=applyEJHOverviewResponse(null,{mode:'full',revision:'history1',data:sample})
const refund=trade('new-refund','live','5','58')
const returned={...waiting,state:'returned',returns:[refund]}
const changedCard={...live,balance:'98.00'}
const fee={...sample.refund_fees[0],amount:'3.40',revision:2}
const final={...sample,cards:[closed,changedCard,dead,external],cases:[returned,...sample.cases.slice(1)],trades:[...sample.trades,refund],refund_fees:[fee],balance:{...sample.balance,data:{abroadList:[{currency:'USD',balance:'599.60'}]}}}
const originalJSON=JSON.stringify(initial)
const updated=applyEJHOverviewResponse(initial,{mode:'delta',revision:'history2',base_revision:'history1',changes:{
 cards:{rows:{order:final.cards.map(c=>c.id),upsert:[changedCard],removed:[]}},
 cases:{rows:{order:final.cases.map(c=>c.id),upsert:[returned],removed:[]}},
 trades:{rows:{order:final.trades.map(c=>c.id),upsert:[refund],removed:[]}},
 refund_fees:{value:[fee]},balance:{value:final.balance}
},removed:[]})
assert.deepEqual(calculateEJHOverview(updated,now),calculateEJHOverview(final,now),'historical finance changed after incremental refund/card/fee updates')
assert.equal(JSON.stringify(initial),originalJSON,'applying a refund modified previous visible totals')
assert.equal(updated.operations.length,4,'closed-card opening fees lost')
assert.equal(updated.consumption_history[0].total,90,'deleted account historical consumption lost')
assert.notDeepEqual(calculateEJHOverview(initial,now).finance,calculateEJHOverview(updated,now).finance,'fixture did not exercise financial changes')
console.log('Passed: full/delta/unchanged, all-history accounting equality, atomic patch, deletion, ordering and mismatched version rejection')
