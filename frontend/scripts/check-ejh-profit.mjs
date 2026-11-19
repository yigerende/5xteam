import assert from 'node:assert/strict'
import { financeProfit, usdToR } from '../src/ejh-profit.js'

const input={openingUSD:'0.50',reversalUSD:'5.56',rechargeUSD:'1.00',consumption:120}
const value=financeProfit(input)
assert.equal(value.openingR,'3.3500')
assert.equal(value.reversalR,'37.2520')
assert.equal(value.rechargeR,'6.7000')
assert.equal(value.costR,'40.6020')
assert.equal(value.profitR,'79.3980')
assert.equal(financeProfit({...input,consumption:0}).profitR,'-40.6020')
assert.equal(financeProfit({...input,consumption:40.602}).profitR,'0.0000')
assert.equal(financeProfit({...input,openingUSD:0,reversalUSD:0,rechargeUSD:0,consumption:0}).profitR,'0.0000')
assert.equal(financeProfit({...input,rechargeUSD:'-1.00'}).costR,'40.6020','signed recharge corrections remain disclosure-only')
assert.equal(financeProfit({...input,rechargeUSD:null}).costR,'40.6020')
assert.equal(financeProfit({...input,rechargeUSD:null}).knownCostR,'40.6020')
for(const override of [{costMissing:1},{consumptionMissing:1},{consumption:null},{consumption:NaN},{reversalUSD:null},{openingUSD:undefined}]){
  assert.equal(financeProfit({...input,...override}).profitR,null,'missing data must not create apparent profit')
}
assert.equal(usdToR('0.01'),'0.0670')
assert.equal(usdToR('-0.01'),'-0.0670')
assert.equal(usdToR('0.00005'),'0.0003')
assert.equal(usdToR('0.0005'),'0.0034')
assert.equal(usdToR('-0.0005'),'-0.0034')
assert.equal(usdToR(null),null)
assert.equal(usdToR('not money'),null)
assert.equal(usdToR('100000000000000.01'),'670000000000000.0670','decimal math must not lose cents at large values')
// Shared recharge fees are excluded from both per-card and ledger profit.
assert.equal(financeProfit({...input,rechargeUSD:0}).profitR,'79.3980')
for(const refundUSD of ['18.14', '0.00', null, 'invalid']) {
  const disclosed=financeProfit({...input,refundUSD})
  assert.equal(disclosed.costUSD,value.costUSD,'refund fees are not operating costs')
  assert.equal(disclosed.profitR,value.profitR,'refund fees are disclosure-only, including unknown amounts')
}
assert.equal(financeProfit({...input,refundUSD:'18.14'}).refundR,'121.5380')
assert.equal(financeProfit({...input,refundUSD:'18.14',refundMissing:1}).refundR,null)
assert.equal(financeProfit({...input,refundUSD:'18.14',refundMissing:1}).profitR,value.profitR)
console.log('Passed: fixed 6.7 conversion, all costs, losses, zero and negative adjustments, incomplete data and exact decimal rounding')

for(const rechargeUSD of ['53.63','0.00','-1.00',null,'invalid']){
 const row=financeProfit({...input,rechargeUSD})
 assert.equal(row.costUSD,value.costUSD)
 assert.equal(row.profitR,value.profitR)
}
assert.equal(financeProfit({...input,rechargeUSD:'53.63'}).rechargeR,'359.3210')
assert.equal(financeProfit({...input,rechargeUSD:'53.63',rechargeMissing:1}).rechargeR,null)
assert.equal(financeProfit({...input,rechargeUSD:'53.63',rechargeMissing:1}).profitR,value.profitR)
