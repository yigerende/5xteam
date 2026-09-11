const methodNames = {
  email_otp: '邮箱验证码登录',
  email_otp_totp: '邮箱验证码 + OpenAI TOTP 登录',
  password_email_otp_totp: 'ChatGPT 密码 + 邮箱验证码 + OpenAI TOTP 登录',
  totp: 'OpenAI TOTP 验证',
  password_totp: 'ChatGPT 密码 + OpenAI TOTP 登录',
  password: 'ChatGPT 密码登录',
  password_email_otp: 'ChatGPT 密码 + 邮箱验证码登录',
  existing_session: '已有认证会话',
  not_started: '尚未执行登录',
}
const authStatuses = { not_started: '未开始验证', running: '验证中', succeeded: '验证通过', failed: '验证失败' }

export function oauthLoginSummary(details = {}) {
  details ||= {}
  if (!details.configured_login_mode && !details.login_method) return ''
  const actual = details.login_method_label || methodNames[details.login_method] || '尚未执行登录'
  const selected = methodNames[details.selected_login_mode] || ''
  const configured = details.configured_login_mode === 'password_totp' ? '优先 2FA' : '邮箱验证码'
  return [
    `实际方式：${actual}`,
    details.login_method === 'not_started' && selected ? `已选择：${selected}` : '',
    `配置：${configured}`,
    authStatuses[details.login_auth_status] || '',
    details.login_method_reason || '',
  ].filter(Boolean).join('；')
}

export function latestOAuthLoginSummary(events = []) {
  const latest = events.find((event) => event.operation === 'oauth' || event.type === 'oauth_protocol' || event.type === 'oauth')
  return latest ? oauthLoginSummary(latest.details) || '历史日志未记录登录方式' : '暂无 OAuth 登录记录'
}
