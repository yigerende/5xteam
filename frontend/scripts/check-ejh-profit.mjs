import assert from 'node:assert/strict'
import { financeProfit, usdToR } from '../src/ejh-profit.js'

const input={openingUSD:'0.50',reversalUSD:'5.56',rechargeUSD:'1.00',consumption:120}
const value=financeProfit(input)
assert.equal(value.openingR,'3.3500')
assert.equal(value.reversalR,'37.2520')
assert.equal(value.rechargeR,'6.7000')
assert.equal(value.costR,'47.3020')
assert.equal(value.profitR,'72.6980')
assert.equal(financeProfit({...input,consumption:0}).profitR,'-47.3020')
assert.equal(financeProfit({...input,consumption:47.302}).profitR,'0.0000')
assert.equal(financeProfit({...input,openingUSD:0,reversalUSD:0,rechargeUSD:0,consumption:0}).profitR,'0.0000')
assert.equal(financeProfit({...input,rechargeUSD:'-1.00'}).costR,'33.9020','signed ledger corrections reduce costs')
assert.equal(financeProfit({...input,rechargeUSD:null}).costR,null)
assert.equal(financeProfit({...input,rechargeUSD:null}).knownCostR,'40.6020')
for(const override of [{costMissing:1},{consumptionMissing:1},{consumption:null},{consumption:NaN},{reversalUSD:null},{rechargeUSD:''},{openingUSD:undefined}]){
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
// Per-card costs deliberately exclude shared recharge fees, which are deducted once in the ledger.
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
