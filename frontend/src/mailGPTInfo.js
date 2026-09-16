export function gptPlanLabel(value) {
  return ({ free: 'Free', plus: 'Plus', pro: 'Pro', team: 'Team', business: 'Business',
    self_serve_business_prolite: 'Team 5x', enterprise: 'Enterprise', edu: 'Edu' })[String(value || '').toLowerCase()] || value || '未获取'
}
export function gptCreatedTime(value) {
  if (!value || !Number.isFinite(Date.parse(value))) return '未获取'
  const parts = new Intl.DateTimeFormat('en-GB', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }).formatToParts(new Date(value))
  const p = Object.fromEntries(parts.map(({ type, value }) => [type, value]))
  return `${p.year}-${p.month}-${p.day} ${p.hour}:${p.minute}:${p.second}`
}
export function gptInfoTitle(account) {
  const check = account.gpt_info_check
  if (!check) return '尚未刷新 GPT 信息'
  const source = ({ entitlement: '账号套餐接口', me: '账号信息接口', jwt: 'AT 声明（非实时）' })[check.plan_source] || '历史数据'
  return [`套餐来源：${source}`, `查询时间：${gptCreatedTime(check.checked_at)}`, check.error].filter(Boolean).join('\n')
}
export const gptInfoStatus = (status) => ({ queued: '排队中', running: '查询中', success: '成功', partial: '部分更新', failed: '失败' })[status] || status
