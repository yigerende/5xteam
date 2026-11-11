// The saved Pro "user" counter, displayed in R without currency conversion.
export function accountConsumption(row) {
  const value = row?.consumption_total
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null
}

export function consumptionText(value) {
  return value === null ? '未查询' : `${value.toFixed(4)} R`
}

export function consumptionNotPushed(row) {
  return accountConsumption(row) === null && row?.consumption_state === 'not_pushed'
}

export function accountConsumptionText(row) {
  return consumptionNotPushed(row) ? '未推送，无消费数据' : consumptionText(accountConsumption(row))
}

// No downstream revenue contributes nothing to profit, but is never presented
// or persisted as a queried zero. Real saved amounts always take precedence.
export function accountConsumptionForProfit(row) {
  return consumptionNotPushed(row) ? 0 : accountConsumption(row)
}

export function summarizeConsumption(rows = [], history = []) {
  const accounts = new Map(), excluded = new Set(), seen = new Set()
  const keyOf = row => row.email?.trim().toLowerCase() || row.id
  for (const row of history) {
    if (seen.has(row.id) || typeof row.total !== 'number' || !Number.isFinite(row.total) || row.total < 0) continue
    seen.add(row.id)
    const key = keyOf(row)
    accounts.set(key, (accounts.get(key) || 0) + row.total)
  }
  for (const row of rows) {
    const key = keyOf(row), value = accountConsumption(row)
    if (value !== null) accounts.set(key, Math.max(accounts.get(key) || 0, value))
    else if (consumptionNotPushed(row)) excluded.add(key)
    else if (!accounts.has(key)) accounts.set(key, null)
  }
  // A different order for the same email may still have an unknown source.
  // History, known zero and unknown applicable rows all override exclusion.
  for (const key of accounts.keys()) excluded.delete(key)
  let total = 0, missing = 0
  for (const value of accounts.values()) { if (value === null) missing++; else total += value }
  return { total: accounts.size && missing === accounts.size ? null : total, missing, count: accounts.size + excluded.size, notPushed: excluded.size }
}

export function cardConsumption(rows = []) {
  const { total, missing, notPushed } = summarizeConsumption(rows)
  return { text: consumptionText(total), total, missing, notPushed }
}
