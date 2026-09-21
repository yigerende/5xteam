export const proPlanName = code => ({ pro5: 'Pro 5x', pro20: 'Pro 20x' })[code] || code
export const payStatusName = status => ({ submitting: '正在提交', created: '已创建', processing: '开通中', success: '成功', failed: '失败', submission_unknown: '提交待确认', manual_review: '待人工核查', not_found: '未找到', reserved: '已冻结', settled: '已结算', waiting: '等待取消续费', pending: '取消续费中' })[status] || status || '—'
export const needsPayPoll = order => !!order?.remote?.id && (!['success', 'failed'].includes(order.status) || ['waiting', 'pending'].includes(order.remote.cancellationStatus))
export const payRequestID = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('')
export function parseCardText(raw) {
  const parts = raw.trim().split('----').map(s => s.trim())
  if (parts.length !== 3) throw new Error('格式应为：银行卡号----年月----CVV')
  const number = parts[0].replace(/[ -]/g, ''), cvv = parts[2]
  const date = parts[1], split = date.split(/[-/\s]+/)
  let year, month
  if (split.length === 2 && split[0].length === 4) [year, month] = split
  else if (split.length === 2) [month, year] = split
  else if (/^\d{6}$/.test(date)) [year, month] = [date.slice(0, 4), date.slice(4)]
  else if (/^\d{4}$/.test(date)) [year, month] = [date.slice(0, 2), date.slice(2)]
  if (!/^\d{12,19}$/.test(number) || !/^\d{3,4}$/.test(cvv)) throw new Error('请检查卡号和 CVV')
  if (!/^\d+$/.test(year || '') || !/^\d+$/.test(month || '')) throw new Error('年月使用 YYYYMM 或 YYYY-MM，也支持 MM/YY')
  const exp_year = Number(year) + (year.length === 2 ? 2000 : 0), exp_month = Number(month)
  if (exp_month < 1 || exp_month > 12) throw new Error('月份应为 1～12')
  return { number, cvv, exp_year, exp_month }
}
