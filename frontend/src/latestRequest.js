// A view owns one list request; polling must not compete with user navigation.
export function createLatestRequest(onLoading = () => {}) {
  let current = null
  let disposed = false
  return {
    run(load, apply, { background = false } = {}) {
      if (disposed) return Promise.resolve()
      if (background && current) return current.promise
      current?.controller.abort()
      const request = { controller: new AbortController(), promise: null }
      current = request
      onLoading(!background)
      request.promise = Promise.resolve().then(async () => {
        try {
          if (request.controller.signal.aborted) return
          const data = await load(request.controller.signal)
          if (current === request && !request.controller.signal.aborted) return await apply(data)
        } catch (error) {
          if (current === request && !request.controller.signal.aborted) throw error
        } finally {
          if (current === request) {
            current = null
            onLoading(false)
          }
        }
      })
      return request.promise
    },
    dispose() {
      disposed = true
      current?.controller.abort()
      current = null
      onLoading(false)
    },
  }
}
