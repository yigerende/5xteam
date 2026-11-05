import { api } from './api.js'

// Only EJH GET reads recover from a deadline once. Financial operations are
// never replayed, and navigation cancellation interrupts both attempts.
export async function readEJH(path, options = {}) {
 if (String(options.method || 'GET').toUpperCase() !== 'GET') throw new Error('仅支持读取请求')
 for (let attempt = 0; ; attempt++) {
  try { return await api(path, options) }
  catch (error) {
   if (!error.timeout || attempt || options.signal?.aborted) throw error
   await new Promise((resolve, reject) => {
    const cancel = () => { clearTimeout(timer); options.signal?.removeEventListener('abort', cancel); reject(new DOMException('请求已取消', 'AbortError')) }
    const timer = setTimeout(() => { options.signal?.removeEventListener('abort', cancel); resolve() }, 300)
    if (options.signal?.aborted) cancel()
    else options.signal?.addEventListener('abort', cancel, { once: true })
   })
  }
 }
}
