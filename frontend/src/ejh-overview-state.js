// Apply transport patches atomically before any financial calculation. Never
// expose partial records or combine a patch with a different snapshot version.
export function applyEJHOverviewResponse(previous, response) {
 if (!response?.mode) return response // compatibility with a server during rollout
 if (response.mode === 'unchanged') {
  if (!previous || previous.revision !== response.revision) throw new Error('余额总览版本已变化，请重新读取')
  return { ...previous, server_time: response.server_time, source_revision: response.source_revision }
 }
 if (response.mode === 'full') {
  return { ...response.data, revision: response.revision, source_revision: response.source_revision, server_time: response.server_time }
 }
 if (response.mode !== 'delta' || !previous || previous.revision !== response.base_revision) throw new Error('余额总览版本已变化，请重新读取')
 const next = { ...previous }
 for (const key of response.removed || []) delete next[key]
 for (const [key, patch] of Object.entries(response.changes || {})) {
  if (Object.hasOwn(patch, 'value')) { next[key] = patch.value; continue }
  const rows = patch.rows
  if (!rows || !Array.isArray(next[key])) throw new Error('余额总览更新不完整，请重新读取')
  const values = new Map(next[key].map(row => [row.id, row]))
  for (const id of rows.removed) values.delete(id)
  for (const row of rows.upsert) values.set(row.id, row)
  if (rows.order.length !== values.size || new Set(rows.order).size !== values.size || rows.order.some(id => !values.has(id))) throw new Error('余额总览更新不完整，请重新读取')
  next[key] = rows.order.map(id => values.get(id))
 }
 return { ...next, revision: response.revision, source_revision: response.source_revision, server_time: response.server_time }
}
