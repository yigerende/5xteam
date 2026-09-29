import { createApp } from 'vue'
import AdminAccountsView from '../src/components/AdminAccountsView.vue'
import '../src/styles.css'

// Isolated browser fixture: every request is local fake data, including errors.
const accounts = [{ id: 'mother', label: '恢复测试母号', email: 'mother@example.com', team_account_id: 'team', current_space_count: 0 }, { id:'mother-two',label:'第二母号',email:'mother-two@example.com',team_account_id:'team-two',current_space_count:0 }]
const serverNow = Date.now() + 86400000
const settings = { join_method: 'mother_invite', remove_method: 'mother_kick', dwell_minutes: 5, operation_interval_seconds: 10, next_interval_seconds: 10, concurrency: 2 }
const makeTask = (id, stage, extra = {}) => ({ id, email: `${id}@example.com`, admin_label: '恢复测试母号', team_id: 'team', stage, status: 'waiting', seat_type: 'prolite', settings: { ...settings, join_method: 'child_request', remove_method: 'child_leave' }, joined_at: new Date(serverNow - 120000).toISOString(), due_at: new Date(serverNow + 180000).toISOString(), message: '正在等待停留时间', ...extra })
const fixture = window.recoveryFixture = {
  requests: [], settings,
  tasks: [makeTask('dwell', 'dwell'), makeTask('failed', 'leave', { status: 'failed', lane: true, seat_type: 'default', message: 'HTTP 429；普通席位继续保留，请重试' }), makeTask('done', 'completed', { status: 'completed', finished: true, seat_type: 'outside', left_at: new Date(serverNow).toISOString(), message: '席位恢复完成' })],
  candidates: [{ id: 'new', email: 'new@example.com', eligible: true, deactivated_time: '2026-09-29T00:00:00Z' }, { id: 'busy', email: 'busy@example.com', eligible: false, reason: '子号已有 Team 邀请在途，恢复任务不会抢占', deactivated_time: '2026-09-29T00:00:00Z' }],
}
const json = data => new Response(JSON.stringify({ ok: true, data }), { headers: { 'Content-Type': 'application/json' } })
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin), method = options.method || 'GET', body = options.body ? JSON.parse(options.body) : {}
  fixture.requests.push({ path: url.pathname, method, body })
  await new Promise(resolve => setTimeout(resolve, 40))
  if (url.pathname === '/api/admin-accounts') return json({ items: accounts, total: accounts.length })
  if (url.pathname === '/api/admin-capacity-snapshots') return json({})
  if (url.pathname === '/api/seat-recovery/settings') { if (method === 'PUT') Object.assign(settings, body); return json(settings) }
  if (url.pathname === '/api/seat-recovery/tasks') {
    const tasks = fixture.tasks.filter(p => url.searchParams.get('active') !== 'true' || !p.finished)
    return json({ items: tasks, total: tasks.length, server_now: new Date(serverNow).toISOString(), running: Object.fromEntries(tasks.filter(p => p.status === 'running').map(p => [p.id, true])) })
  }
  if (/^\/api\/admin-accounts\/mother(-two)?\/seat-recovery$/.test(url.pathname)) {
    const second = url.pathname.includes('mother-two')
    if (method === 'GET') return json({ items: second ? [{...fixture.candidates[0],id:'another',email:'another@example.com'}] : fixture.candidates, team_id: second ? 'team-two' : 'team' })
    const tasks = body.emails.map(email => makeTask(email.split('@')[0], 'login', { email, team_id:body.team_id,admin_label:second?'第二母号':'恢复测试母号',settings: { ...settings }, joined_at: null, due_at: null, status: 'running', message: '正在检测 AT，有效则直接使用' }))
    fixture.tasks.unshift(...tasks); return json({ items: tasks, failures: {} })
  }
  const control = url.pathname.match(/\/tasks\/([^/]+)\/control$/)
  if (control) {
    const p = fixture.tasks.find(p => p.id === control[1])
    if (body.action === 'pause') p.paused = true
    if (body.action === 'resume') p.paused = false
    if (body.action === 'retry') { p.status = 'running'; p.message = '正在核实实际成员后重试退出' }
    if (body.action === 'cancel') { p.finished = true; p.status = 'cancelled' }
    return json(p)
  }
  if (url.pathname.endsWith('/logs')) return json({ items: [{ id: 2, at: new Date(serverNow).toISOString(), stage: 'leave', message: '子号退出（全局代理）：HTTP 429，普通席位串行位置继续保留' }, { id: 1, at: new Date(serverNow - 1000).toISOString(), stage: 'switch', message: '母号专属代理：已实时确认普通席位' }] })
  throw new Error(`Unexpected request: ${method} ${url.pathname}`)
}
createApp(AdminAccountsView, { accounts, proxies: [] }).mount('#app')
