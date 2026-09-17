export function canJoinAdmin(account, admin) {
  if (!admin.rotation_disabled) return true
  return account.remove_status !== 'completed' && !account.remote_removed_at &&
    Boolean(account.team_account_id) && account.admin_account_id === admin.id &&
    account.team_account_id === admin.team_account_id &&
    ['running', 'completed'].some(status => account.invite_status === status || account.accept_status === status)
}

export function rotationSeatSummary(admins, snapshots, summary) {
  const disabled = new Set(admins.filter(admin => admin.rotation_disabled).map(admin => String(admin.id)))
  let total = 0, inside = 0, insidePremium = 0, quotaCount = 0, quotaRemaining = 0, pending = 0
  for (const admin of admins) {
    if (disabled.has(String(admin.id))) continue
    const capacity = snapshots.get(admin.id) || snapshots.get(String(admin.id))
    total += Number(capacity?.premium?.total || 0)
  }
  for (const [id, usage] of Object.entries(summary.seat_usage_by_admin || {})) {
    if (disabled.has(id)) continue
    inside += Number(usage.inside || 0)
    insidePremium += Number(usage.inside_premium || 0)
    quotaCount += Number(usage.quota_count || 0)
    quotaRemaining += Number(usage.quota_remaining || 0)
  }
  for (const [id, usage] of Object.entries(summary.pending_seats_by_admin || {})) {
    if (!disabled.has(id)) pending += Number(usage.premium || 0)
  }
  return { total, remaining: Math.max(0, total - insidePremium - pending), inside, insidePremium, pending, quotaCount, quotaRemaining }
}
