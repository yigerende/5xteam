export function canJoinAdmin(account, admin) {
  if (!admin.rotation_disabled) return true
  return account.remove_status !== 'completed' && !account.remote_removed_at &&
    Boolean(account.team_account_id) && account.admin_account_id === admin.id &&
    account.team_account_id === admin.team_account_id &&
    ['running', 'completed'].some(status => account.invite_status === status || account.accept_status === status)
}
