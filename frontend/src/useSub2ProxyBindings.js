import {computed, onBeforeUnmount, ref, watch} from 'vue'
import {api} from './api'
import {proxyReleasePending, proxyBindingID} from './sub2ProxyRelease'

// One bounded local query per visible page; never one request per row or tick.
export function useSub2ProxyBindings(getRows) {
  const proxyBindings = ref({}), proxyNow = ref(Date.now()), proxyBindingError = ref('')
  const proxyRetrying = ref('')
  const ids = computed(() => [...new Set(getRows().map(proxyBindingID).filter(Boolean))].sort().join(','))
  let controller, lastRead = 0, offset = 0, disposed = false
  async function refresh() {
    if (disposed || !ids.value || document.hidden || controller) return
    const key = ids.value, request = new AbortController()
    controller = request; lastRead = Date.now()
    try {
      const result = await api(`/api/sub2-proxy-bindings?ids=${encodeURIComponent(key)}`, {signal: request.signal})
      if (disposed || request.signal.aborted || key !== ids.value) return
      proxyBindings.value = result.items || {}
      const server = Date.parse(result.server_time)
      if (Number.isFinite(server)) offset = server - Date.now()
      proxyNow.value = Date.now() + offset; proxyBindingError.value = ''
    } catch (e) {
      if (!request.signal.aborted && !disposed) proxyBindingError.value = e.message || '代理状态读取失败'
    } finally { if (controller === request) controller = null }
  }
  watch(ids, () => {
    controller?.abort(); controller = null; proxyBindings.value = {}; proxyBindingError.value = ''; lastRead = 0
    void refresh()
  }, {immediate: true})
  const ticker = setInterval(() => {
    if (document.hidden || !ids.value) return
    proxyNow.value = Date.now() + offset
    if (Date.now() - lastRead >= 10000 && ids.value.split(',').some(id => proxyReleasePending(proxyBindings.value[id]))) void refresh()
  }, 1000)
  async function retryProxyBinding(id) {
    if (proxyRetrying.value) return
    proxyRetrying.value = id
    try {await api(`/api/sub2-proxy-bindings/${encodeURIComponent(id)}/retry`, {method:'POST',body:{}}); await refresh()}
    catch (e) {proxyBindingError.value = e.message || '重试提交失败'}
    finally {proxyRetrying.value = ''}
  }
  onBeforeUnmount(() => {disposed = true; clearInterval(ticker); controller?.abort()})
  return {proxyBindings, proxyNow, proxyBindingError, proxyRetrying, retryProxyBinding}
}
