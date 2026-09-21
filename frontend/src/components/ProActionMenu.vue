<script setup>
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { MoreHorizontal } from 'lucide-vue-next'

const props = defineProps({ email: { type: String, required: true } })
const opened = ref(false), trigger = ref(null), menu = ref(null), position = ref({})
const menuID = `pro-actions-${encodeURIComponent(props.email)}`
function close(restoreFocus = false) {
  opened.value = false
  if (restoreFocus) trigger.value?.focus({ preventScroll: true })
}
async function toggle() {
  if (opened.value) return close()
  opened.value = true
  const rect = trigger.value.getBoundingClientRect()
  const width = Math.min(244, window.innerWidth - 16)
  position.value = { width: `${width}px`, maxHeight: `${Math.min(480, window.innerHeight - 16)}px`, left: `${Math.max(8, Math.min(rect.right - width, window.innerWidth - width - 8))}px`, top: '8px' }
  await nextTick()
  if (!opened.value || !menu.value) return
  reposition()
  await nextTick()
  menu.value?.querySelector('button:not(:disabled)')?.focus({ preventScroll: true })
}
function outside(event) {
  if (!trigger.value?.contains(event.target) && !menu.value?.contains(event.target)) close()
}
function reposition() {
  if (!opened.value || !menu.value || !trigger.value) return
  const rect = trigger.value.getBoundingClientRect()
  const { height, width } = menu.value.getBoundingClientRect()
  const top = rect.bottom + height + 8 <= window.innerHeight ? rect.bottom + 4 : rect.top - height - 4
  position.value = { ...position.value, left: `${Math.max(8, Math.min(rect.right - width, window.innerWidth - width - 8))}px`, top: `${Math.max(8, Math.min(top, window.innerHeight - height - 8))}px` }
}
function scroll(event) { if (!menu.value?.contains(event.target)) reposition() }
function choose(event) {
  const button = event.target.closest('button')
  if (button && !button.disabled) close()
}
function keydown(event) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(true) }
}
function resize() { close() }
function detach() {
  document.removeEventListener('click', outside)
  window.removeEventListener('scroll', scroll, true)
  window.removeEventListener('resize', resize)
}
watch(opened, value => {
  detach()
  if (!value) return
  document.addEventListener('click', outside)
  window.addEventListener('scroll', scroll, true)
  window.addEventListener('resize', resize)
})
onBeforeUnmount(detach)
</script>

<template>
  <button ref="trigger" type="button" class="icon-button" title="更多操作" :aria-label="`${email} 更多操作`" :aria-expanded="opened" :aria-controls="opened ? menuID : undefined" @click="toggle" @keydown="keydown"><MoreHorizontal :size="17" /></button>
  <Teleport to="body">
    <div v-if="opened" :id="menuID" ref="menu" class="pro-action-menu" role="group" :aria-label="`${email} 账号操作`" :style="position" @click="choose" @keydown="keydown">
      <slot />
    </div>
  </Teleport>
</template>

<style scoped>
.pro-action-menu { position:fixed;z-index:1000;overflow-y:auto;padding:6px;border:1px solid var(--line);border-radius:8px;background:var(--surface);box-shadow:0 12px 36px rgba(0,0,0,.16); }
.pro-action-menu :deep(button) { display:flex;align-items:center;gap:9px;width:100%;min-height:34px;padding:8px 10px;border-radius:5px;text-align:left;font-size:12px;color:var(--text);white-space:nowrap; }
.pro-action-menu :deep(button:hover:not(:disabled)),.pro-action-menu :deep(button:focus-visible) { background:var(--surface-2);outline:2px solid var(--line);outline-offset:-2px; }
.pro-action-menu :deep(button:disabled) { opacity:.4;cursor:not-allowed; }
.pro-action-menu :deep(svg) { flex-shrink:0; }
</style>
