export function createEJHOverviewWorker() {
 let worker, sequence = 0, disposed = false
 const pending = new Map()
 function fail(error) { for (const job of pending.values()) job.reject(error); pending.clear() }
 return {
  async calculate(source) {
   if (disposed) throw new Error('页面已关闭')
   if (typeof Worker === 'undefined') {
    const { calculateEJHOverview } = await import('./ejh-overview.js')
    return calculateEJHOverview(source)
   }
   if (!worker) {
    worker = new Worker(new URL('./ejh-overview.worker.js', import.meta.url), { type: 'module' })
    worker.onmessage = ({ data }) => {
     const job = pending.get(data.id); if (!job) return
     pending.delete(data.id)
     if (data.error) job.reject(new Error(data.error)); else job.resolve(data.summary)
    }
    worker.onerror = () => { fail(new Error('余额总览计算失败，保留上次结果')); worker?.terminate(); worker = null }
   }
   const id = ++sequence
   return new Promise((resolve, reject) => { pending.set(id, { resolve, reject }); worker.postMessage({ id, source }) })
  },
  dispose() { disposed = true; worker?.terminate(); worker = null; fail(new Error('页面已关闭')) },
 }
}
