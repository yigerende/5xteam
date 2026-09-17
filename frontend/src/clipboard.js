// Clipboard API requires HTTPS/localhost. Keep a user-initiated fallback for HTTP/IP deployments.
export async function copyText(value) {
  const text = String(value ?? '')
  if (!text) return
  try {
    if (window.isSecureContext !== false && typeof navigator.clipboard?.writeText === 'function') {
      await navigator.clipboard.writeText(text)
      return
    }
  } catch {
    // Permission/policy restrictions can also reject the modern API on HTTPS.
  }
  const active = document.activeElement
  const selection = window.getSelection?.()
  const ranges = []
  if (selection) {
    for (let index = 0; index < selection.rangeCount; index++) ranges.push(selection.getRangeAt(index).cloneRange())
  }
  const inputSelection = active && typeof active.selectionStart === 'number'
    ? [active.selectionStart, active.selectionEnd, active.selectionDirection]
    : null
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.readOnly = true
  textarea.tabIndex = -1
  textarea.setAttribute('aria-label', '临时复制区域')
  Object.assign(textarea.style, { position: 'fixed', left: '0', top: '0', width: '1px', height: '1px', opacity: '0', pointerEvents: 'none' })
  try {
    // A native modal dialog makes the rest of the document inert.
    const parent = active?.closest?.('dialog[open]') || document.body
    parent.appendChild(textarea)
    textarea.focus({ preventScroll: true })
    textarea.select()
    textarea.setSelectionRange(0, text.length)
    if (typeof document.execCommand !== 'function' || !document.execCommand('copy')) {
      throw new Error('浏览器禁止复制，请选中文本后手动复制')
    }
  } finally {
    textarea.value = ''
    textarea.remove()
    if (active?.isConnected) {
      active.focus({ preventScroll: true })
      if (inputSelection) active.setSelectionRange(...inputSelection)
      else if (selection && ranges.length) {
        selection.removeAllRanges()
        for (const range of ranges) selection.addRange(range)
      }
    }
  }
}
