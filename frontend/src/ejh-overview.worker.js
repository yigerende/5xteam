import { calculateEJHOverview } from './ejh-overview.js'
self.onmessage = ({ data: { id, source } }) => {
 try { self.postMessage({ id, summary: calculateEJHOverview(source) }) }
 catch (error) { self.postMessage({ id, error: error.message || '余额总览计算失败' }) }
}
