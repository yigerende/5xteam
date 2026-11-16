// Reuse financial inputs when switching dates. Heavy processing runs outside
// Vue's reactive graph and outside the browser's rendering thread.
export function createEJHFinanceWorker() {
 let worker, sequence = 0, disposed = false, source, history, prepared
 const pending = new Map()
 function fail(error) { for (const job of pending.values()) job.reject(error); pending.clear() }
 return {
  async calculate(nextSource, nextHistory, mode, date) {
   if (disposed) throw new Error('页面已关闭')
   const changed = source !== nextSource || history !== nextHistory
   if (typeof Worker === 'undefined') {
    const { financeReport, prepareFinanceReport } = await import('./ejh-finance.js')
    if (changed || !prepared) prepared = prepareFinanceReport(nextSource, nextHistory)
    source = nextSource; history = nextHistory
    return financeReport(source, history, mode, date, prepared)
   }
   if (!worker) {
    worker = new Worker(new URL('./ejh-finance.worker.js', import.meta.url), { type: 'module' })
    worker.onmessage = ({ data }) => {
     const job = pending.get(data.id); if (!job) return
     pending.delete(data.id)
     if (data.error) { source = null; job.reject(new Error(data.error)) } else job.resolve(data.report)
    }
    worker.onerror = () => {
     fail(new Error('成本统计计算失败，保留上次结果'))
     worker?.terminate(); worker = null; source = null
    }
   }
   const input = { mode, date }
   if (changed || !source) { input.source = nextSource; input.history = nextHistory }
   source = nextSource; history = nextHistory
   const id = ++sequence
   return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject })
    try { worker.postMessage({ id, input }) } catch (error) { pending.delete(id); source = null; reject(error) }
   })
  },
  dispose() { disposed = true; worker?.terminate(); worker = null; fail(new Error('页面已关闭')) },
 }
}
