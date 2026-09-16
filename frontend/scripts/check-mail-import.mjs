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

assert.deepEqual(
  parseMailAccountText('fourth@gmail.com----chatgpt-password----2fa:key-with-vendor-format----opaque-access-token'),
  [{
    email: 'fourth@gmail.com',
    gpt_password: 'chatgpt-password',
    totp_secret: '2fa:key-with-vendor-format',
    access_token: 'opaque-access-token',
  }],
)

assert.deepEqual(
  parseMailAccountText('person@hotmail.co.uk----mail-password----00000000-0000-4000-8000-000000000000----outlook-refresh-token'),
  [{
    email: 'person@hotmail.co.uk',
    mail_password: 'mail-password',
    client_id: '00000000-0000-4000-8000-000000000000',
    mail_refresh_token: 'outlook-refresh-token',
  }],
)

assert.deepEqual(
  parseMailAccountText('hosted@company.example----mail-password----00000000-0000-4000-8000-000000000000----outlook-refresh-token'),
  [{
    email: 'hosted@company.example',
    mail_password: 'mail-password',
    client_id: '00000000-0000-4000-8000-000000000000',
    mail_refresh_token: 'outlook-refresh-token',
  }],
)

const email = 'format@example.com'
const password = 'sample-password$$'
const totp = 'JBSWY3DPEHPK3PXP'
const pickup = 'https://mail.example/messages/token/format@example.com'
const clientID = '00000000-0000-4000-8000-000000000000'
const accessToken = 'token---segment----other-segment'
const refreshToken = 'refresh---segment----other-segment'
const sessionWithSeparators = JSON.stringify({
  user: { email, name: 'Example--- User---- Name, With Comma' },
  accessToken,
  sessionToken: 'must-not-be-forwarded',
})
const formats = [
  { fields: [email, password], expected: { email, mail_password: password } },
  { fields: [email, pickup], expected: { email, pickup_url: pickup } },
  { fields: [email, password, pickup], expected: { email, mail_password: password, pickup_url: pickup } },
  { fields: [email, pickup, sessionWithSeparators], expected: { email, pickup_url: pickup, access_token: accessToken } },
  { fields: [email, password, pickup, sessionWithSeparators], expected: { email, mail_password: password, pickup_url: pickup, access_token: accessToken } },
  { fields: [email, password, totp], expected: { email, gpt_password: password, totp_secret: totp } },
  { fields: [email, password, totp, accessToken], expected: { email, gpt_password: password, totp_secret: totp, access_token: accessToken } },
  { fields: ['format@outlook.com', password, 'vendor-client', refreshToken], expected: { email: 'format@outlook.com', mail_password: password, client_id: 'vendor-client', mail_refresh_token: refreshToken } },
  { fields: [email, password, clientID, refreshToken], expected: { email, mail_password: password, client_id: clientID, mail_refresh_token: refreshToken } },
]
for (const separator of ['---', '----', '\t', ',']) {
  for (const { fields, expected } of formats) {
    assert.deepEqual(parseMailAccountText(fields.join(separator)), [expected])
  }
  assert.throws(
    () => parseMailAccountText([email, pickup, JSON.stringify({ user: { email } })].join(separator)),
    /缺少 accessToken/,
  )
  assert.throws(
    () => parseMailAccountText([email, pickup, JSON.stringify({ user: { email: 'other@example.com' }, accessToken })].join(separator)),
    /邮箱与账号邮箱不一致/,
  )
}

assert.deepEqual(
  parseMailAccountText('\uFEFF  triple@example.com---sample-password$$---' + totp + '\r\n\r\n  quad@example.com----sample-password$$----' + totp + '  '),
  [
    { email: 'triple@example.com', gpt_password: password, totp_secret: totp },
    { email: 'quad@example.com', gpt_password: password, totp_secret: totp },
  ],
)
assert.deepEqual(
  parseMailAccountText([email, 'password---with-three-dashes', totp].join('----')),
  [{ email, gpt_password: 'password---with-three-dashes', totp_secret: totp }],
)
const jsonAccount = { email, gpt_password: 'password---and----dashes', totp_secret: totp }
for (const input of [jsonAccount, [jsonAccount], { accounts: [jsonAccount] }]) {
  assert.deepEqual(parseMailAccountText(JSON.stringify(input)), [jsonAccount])
}


console.log('mail import parser: ok')
