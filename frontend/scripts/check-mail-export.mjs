import assert from 'node:assert/strict'
import { readMailExport } from '../src/mailExport.js'

const encoder = new TextEncoder()
const payload = new Uint8Array([0, 1, 10, 13, 255, 200, 45, 10])
const events = [
  { type: 'progress', stage: 'processing', processed: 0, total: 501 },
  { type: 'progress', stage: 'processing', processed: 25, total: 501 },
  { type: 'progress', stage: 'generating', processed: 501, total: 501 },
  { type: 'file', size: payload.length, headers: { 'Content-Type': 'application/zip', 'Content-Disposition': 'attachment; filename="test.zip"', 'X-Export-Missing-AT': '2' } },
]
const metadata = encoder.encode(events.map(e => JSON.stringify(e) + '\n').join(''))
const all = new Uint8Array(metadata.length + payload.length)
all.set(metadata)
all.set(payload, metadata.length)
function response(bytes, chunkSize) {
  return new Response(new ReadableStream({
    start(controller) {
      for (let i = 0; i < bytes.length; i += chunkSize) controller.enqueue(bytes.slice(i, i + chunkSize))
      controller.close()
    },
  }), { headers: { 'Content-Type': 'application/x-mail-export' } })
}
for (const size of [1, 2, 7, 100, all.length]) {
  const progress = []
  const result = await readMailExport(response(all, size), e => progress.push(e))
  assert.deepEqual(new Uint8Array(await result.blob.arrayBuffer()), payload)
  assert.equal(result.headers.get('X-Export-Missing-AT'), '2')
  assert.deepEqual(progress.filter(e => e.type === 'progress'), events.slice(0, 3))
  assert.equal(progress.at(-1).received, payload.length)
}
await assert.rejects(readMailExport(response(all.slice(0, -1), 3), () => {}), /连接中断/)
await assert.rejects(readMailExport(response(metadata.slice(0, -1), 3), () => {}), /连接中断/)
await assert.rejects(readMailExport(response(encoder.encode('{"type":"error","error":"账号已删除"}\n'), 1), () => {}), /账号已删除/)
await assert.rejects(readMailExport(new Response('{"error":"登录已过期"}', { status: 401 }), () => {}), /登录已过期/)
await assert.rejects(readMailExport(new Response('<html>Proxy error</html>'), () => {}), /响应格式异常/)
console.log('Mail export: chunk boundaries, binary data, progress, missing counts, truncation and errors passed')
