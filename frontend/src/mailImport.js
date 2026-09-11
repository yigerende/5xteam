function isHTTPURL(value) {
  return /^https?:\/\//i.test(value || '')
}

function emailDomain(value) {
  const email = String(value || '').trim().toLowerCase()
  const at = email.lastIndexOf('@')
  return at > 0 && at < email.length - 1 ? email.slice(at + 1) : ''
}

function isMicrosoftMailboxDomain(domain) {
  return /^(?:[a-z0-9-]+\.)*(?:outlook|hotmail|live|msn)\.[a-z0-9.-]+$/i.test(domain || '')
}

function isOAuthClientID(value) {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(String(value || '').trim())
}

function parseSessionJSON(value, lineNumber) {
  const text = String(value || '').trim()
  if (!text) return null
  if (!text.startsWith('{') && !text.startsWith('[')) {
    throw new Error(`第 ${lineNumber} 行 Session JSON 无效`)
  }
  let parsed
  try {
    parsed = JSON.parse(text)
  } catch {
    throw new Error(`第 ${lineNumber} 行 Session JSON 格式无效`)
  }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error(`第 ${lineNumber} 行 Session JSON 必须是对象`)
  }
  const accessToken = String(parsed.accessToken || parsed.access_token || '').trim()
  if (!accessToken) throw new Error(`第 ${lineNumber} 行 Session JSON 缺少 accessToken`)
  const sessionEmail = String(parsed?.user?.email || parsed.email || '').trim().toLowerCase()
  return { access_token: accessToken, sessionEmail }
}

function splitAccountLine(line) {
  // Keep commas inside trailing Session JSON untouched.
  if (line.includes('----')) return line.split('----').map((item) => item.trim())
  if (line.includes('\t')) return line.split('\t').map((item) => item.trim())
  return line.split(',').map((item) => item.trim())
}

export function parseMailAccountText(rawValue) {
  const raw = String(rawValue || '').trim()
  if (!raw) throw new Error('请填写邮件账号')
  if (raw.startsWith('[') || raw.startsWith('{')) {
    const parsed = JSON.parse(raw)
    return (Array.isArray(parsed) ? parsed : parsed.accounts || [parsed]).filter((item) => item?.email)
  }
  return raw.split(/\r?\n/).map((line) => line.trim()).filter(Boolean).map((line, index) => {
    const parts = splitAccountLine(line)
    const email = parts[0]
    const domain = emailDomain(email)
    if (!domain) throw new Error(`第 ${index + 1} 行邮箱无效`)

    // Link-based mailboxes may optionally put a password/query code before the URL.
    const urlIndex = parts.findIndex((item, partIndex) => partIndex > 0 && isHTTPURL(item))
    if (urlIndex > 0) {
      const sessionText = parts.slice(urlIndex + 1).join('----').trim()
      const session = sessionText ? parseSessionJSON(sessionText, index + 1) : null
      if (session?.sessionEmail && session.sessionEmail !== email.toLowerCase()) {
        throw new Error(`第 ${index + 1} 行 Session 邮箱与账号邮箱不一致`)
      }
      return {
        email,
        pickup_url: parts[urlIndex],
        ...(urlIndex >= 2 && parts[1] ? { mail_password: parts[1] } : {}),
        ...(session?.access_token ? { access_token: session.access_token } : {}),
      }
    }
    if (parts.length >= 4) {
      // Both Outlook OAuth and TOTP + AT use four fields. Resolve the shape
      // from the mailbox domain; the UUID fallback keeps Microsoft-hosted
      // custom domains compatible with earlier imports.
      if (isMicrosoftMailboxDomain(domain) || isOAuthClientID(parts[2])) {
        return { email, mail_password: parts[1], client_id: parts[2], mail_refresh_token: parts.slice(3).join('----') }
      }
      return { email, gpt_password: parts[1], totp_secret: parts[2], access_token: parts.slice(3).join('----') }
    }
    if (parts.length === 3) return { email, gpt_password: parts[1], totp_secret: parts[2] }
    return { email, mail_password: parts[1] || '' }
  })
}
