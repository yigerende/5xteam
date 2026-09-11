import assert from 'node:assert/strict'
import { oauthLoginSummary, latestOAuthLoginSummary } from '../src/oauthLoginLog.js'

const details = { configured_login_mode: 'password_totp', selected_login_mode: 'email_otp', login_method: 'email_otp', login_auth_status: 'succeeded', login_method_reason: '缺少 ChatGPT 密码，使用邮箱验证码登录' }
assert.match(oauthLoginSummary(details), /实际方式：邮箱验证码登录/)
assert.match(oauthLoginSummary(details), /缺少 ChatGPT 密码/)
assert.match(oauthLoginSummary(details), /验证通过/)
assert.equal(oauthLoginSummary(null), '')
assert.equal(latestOAuthLoginSummary([]), '暂无 OAuth 登录记录')
assert.match(latestOAuthLoginSummary([{ operation: 'oauth' }, { type: 'oauth_protocol', details }]), /历史日志未记录/)
assert.match(latestOAuthLoginSummary([{ operation: 'remove' }, { operation: 'oauth', details }]), /邮箱验证码登录/)
assert.match(oauthLoginSummary({ ...details, login_method: 'not_started', login_method_label: '尚未执行登录', login_auth_status: 'not_started' }), /尚未执行登录；已选择：邮箱验证码登录/)
assert.match(oauthLoginSummary({ ...details, login_method: 'password_totp', login_auth_status: 'failed', login_method_reason: '' }), /TOTP 登录.*验证失败/)
assert.match(oauthLoginSummary({ ...details, login_method: 'email_otp_totp' }), /实际方式：邮箱验证码 \+ OpenAI TOTP 登录/)
assert.match(oauthLoginSummary({ ...details, login_method: 'password_email_otp_totp' }), /实际方式：ChatGPT 密码 \+ 邮箱验证码 \+ OpenAI TOTP 登录/)
console.log('OAuth login log display: ok')
