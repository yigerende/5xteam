import assert from 'node:assert/strict'
import http from 'node:http'
import { api } from '../src/api.js'
import { readEJH } from '../src/ejh-read.js'

const counts=new Map()
const server = http.createServer((req, res) => {
  const n=(counts.get(req.url)||0)+1;counts.set(req.url,n)
  if(req.url==='/drop-always'||req.url==='/drop-once'&&n===1||req.url==='/post-drop'){req.socket.destroy();return}
  if(req.url==='/drop-body'&&n===1){res.writeHead(200,{'Content-Type':'application/json','Content-Length':'300'});res.write('{');setTimeout(()=>res.destroy(),10);return}
  if (req.url === '/stall' || req.url === '/ejh-timeout' || req.url === '/ejh-once' && n === 1 || req.url === '/ejh-cancel') return
  res.setHeader('Content-Type', 'application/json')
  if (req.url === '/body-stall') { res.write('{"ok":'); return }
  if (req.url === '/failure') { res.writeHead(409); res.end(JSON.stringify({ok:false,error:'conflict'})); return }
  res.end(JSON.stringify({ok:true,data:{method:req.method}}))
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const base = `http://127.0.0.1:${server.address().port}`
try {
  for (const path of ['/stall', '/body-stall']) {
    await assert.rejects(api(base+path, {timeoutMs:100}), /读取数据超时/)
  }
  const controller = new AbortController()
  const cancelled = api(base+'/stall', {signal:controller.signal,timeoutMs:1000})
  controller.abort()
  await assert.rejects(cancelled, error => !error.message.includes('读取数据超时'))
  await assert.rejects(api(base+'/failure', {timeoutMs:1000}), error => error.status === 409 && error.message === 'conflict')
  assert.deepEqual(await api(base+'/', {timeoutMs:1000}), {method:'GET'})
  assert.deepEqual(await api(base+'/', {method:'POST',body:{}}), {method:'POST'})
  assert.deepEqual(await api(base+'/drop-once',{timeoutMs:2000}),{method:'GET'})
  assert.equal(counts.get('/drop-once'),2)
  assert.deepEqual(await api(base+'/drop-body',{timeoutMs:2000}),{method:'GET'})
  assert.equal(counts.get('/drop-body'),2)
  await assert.rejects(api(base+'/drop-always',{timeoutMs:2000}),e=>e.network&&e.message.includes('网络连接中断'))
  assert.equal(counts.get('/drop-always'),2)
  await assert.rejects(api(base+'/post-drop',{method:'POST',body:{}}),/避免重复提交/)
  assert.equal(counts.get('/post-drop'),1,'write replayed after lost response')
  assert.equal(counts.get('/failure'),1,'business error retried')
  assert.ok(counts.get('/stall')<=2,'timeout/cancellation retried')
  assert.deepEqual(await readEJH(base+'/ejh-once',{timeoutMs:100}),{method:'GET'})
  assert.equal(counts.get('/ejh-once'),2)
  await assert.rejects(readEJH(base+'/ejh-timeout',{timeoutMs:100}),e=>e.timeout)
  assert.equal(counts.get('/ejh-timeout'),2,'unbounded timeout retries')
  const stop=new AbortController(),waiting=readEJH(base+'/ejh-cancel',{timeoutMs:100,signal:stop.signal})
  setTimeout(()=>stop.abort(),150)
  await assert.rejects(waiting,e=>e.name==='AbortError')
  assert.equal(counts.get('/ejh-cancel'),1,'cancelled retry ran')
  await assert.rejects(readEJH(base+'/',{method:'POST'}),/仅支持读取/)
  console.log('Passed: EJH-only timeout recovery once, retry cancellation, write prohibition')
  console.log('Passed: transport loss retry once for reads, shared deadline, no write or HTTP error replay')
  console.log('Passed: real HTTP header/body timeout, navigation cancellation, status preservation, normal reads and unchanged writes')
} finally {
  server.closeAllConnections()
  await new Promise(resolve => server.close(resolve))
}
