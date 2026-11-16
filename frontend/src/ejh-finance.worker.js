import { financeReport, prepareFinanceReport } from './ejh-finance.js'

let source, history, prepared
self.onmessage = ({ data: { id, input } }) => {
 try {
  if (input.source) {
   source = input.source; history = input.history
   prepared = prepareFinanceReport(source, history)
  }
  if (!prepared) throw new Error('成本记录尚未读取')
  self.postMessage({ id, report: financeReport(source, history, input.mode, input.date, prepared) })
 } catch (error) { self.postMessage({ id, error: error.message || '成本统计计算失败' }) }
}
