import assert from 'node:assert/strict'
import { parseMailAccountText } from '../src/mailImport.js'

assert.deepEqual(
  parseMailAccountText('first@example.com----https://mail.example/messages/token/first@example.com'),
  [{ email: 'first@example.com', pickup_url: 'https://mail.example/messages/token/first@example.com' }],
)

assert.deepEqual(
  parseMailAccountText('second@example.com----query-code----https://mail.example/messages/token/second@example.com'),
  [{
    email: 'second@example.com',
    mail_password: 'query-code',
    pickup_url: 'https://mail.example/messages/token/second@example.com',
  }],
)

const session = JSON.stringify({
  user: { email: 'session@example.com', name: 'Example, User' },
  account: { id: 'account-1', planType: 'free' },
  accessToken: 'temporary-at',
  sessionToken: 'must-not-be-forwarded',
})
assert.deepEqual(
  parseMailAccountText(`session@example.com----https://mail.example/messages/token/session@example.com----${session}`),
  [{
    email: 'session@example.com',
    pickup_url: 'https://mail.example/messages/token/session@example.com',
    access_token: 'temporary-at',
  }],
)

assert.throws(
  () => parseMailAccountText('missing@example.com----https://mail.example/messages/token/missing@example.com----{"user":{"email":"missing@example.com"}}'),
  /缺少 accessToken/,
)
assert.throws(
  () => parseMailAccountText('wrong@example.com----https://mail.example/messages/token/wrong@example.com----{"user":{"email":"other@example.com"},"accessToken":"temporary-at"}'),
  /邮箱与账号邮箱不一致/,
)

assert.deepEqual(
  parseMailAccountText('third@example.com----gpt-password----JBSWY3DPEHPK3PXP'),
  [{ email: 'third@example.com', gpt_password: 'gpt-password', totp_secret: 'JBSWY3DPEHPK3PXP' }],
)

console.log('mail import parser: ok')
