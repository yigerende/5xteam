import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { ref } from 'vue'
const source = readFileSync(new URL('../src/components/ProSIWCMarker.vue', import.meta.url), 'utf8')
const script = source.split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm, '')
const calls = [], events = []
let fail = true
const ctx = vm.createContext({ ref, defineProps: () => ({ account: { email: 'test@example.com' } }), defineEmits: () => (...args) => events.push(args), async api(path, options) { calls.push({ path, options }); if (fail) throw Error('暂时不可用') } })
vm.runInContext(script + ';globalThis.test={retry,busy,error}', ctx)
await ctx.test.retry()
assert.equal(ctx.test.error.value, '暂时不可用'); assert.equal(ctx.test.busy.value, false)
fail = false; await ctx.test.retry()
assert.equal(events[0][0], 'updated')
assert.equal(calls.length, 2)
for (const call of calls) { assert.equal(call.path, '/api/pro-accounts/test%40example.com/siwc-marker/retry'); assert.equal(call.options.method, 'POST') }
assert.match(source, /account.siwc_marker.state === 'retry'/)
console.log('Passed: marker retry isolation, visible errors, retry recovery')
