<script setup>
import {ref,onBeforeUnmount} from 'vue'
import {api} from '../api'
import {copyText} from '../clipboard'
const props=defineProps({settings:{type:Object,required:true},testing:{type:Boolean,default:false}})
const emit=defineEmits(['updated','test'])
const busy=ref(false),error=ref(''),notice=ref(''),privateKey=ref('')
async function save(importKey=false,regenerate=false){

 busy.value=true;error.value='';notice.value=''
 try{
  const cfg=await api('/api/ejh/signing-key',{method:'POST',body:importKey?{private_key:privateKey.value}:regenerate?{regenerate:true}:{}})
  privateKey.value='';emit('updated',cfg);notice.value='密钥已加密保存。请把下方公钥填入 EJH 官网的「接口配置 → 配置公钥」，官网会重新生成 secretKey，请将新值填入本页 AppSecret 并保存，再点击「测试连接」。'
 }catch(e){error.value=e.message}finally{busy.value=false}
}
async function copy(){try{await copyText(props.settings.signing_public_key);notice.value='公钥已复制，请粘贴到 EJH 官网的「配置公钥」。'}catch{error.value='复制失败，请选择下方公钥手动复制。'}}
async function regenerate(){if(window.confirm('重新生成会更换 Space 当前公私钥。请随后把新公钥更新到 EJH 官网，并将官网新生成的 secretKey 保存到 Space 的 AppSecret。两边更新完成前接口将无法通过认证。是否重新生成？'))await save(false,true)}
onBeforeUnmount(()=>{privateKey.value=''})
</script>
<template>
 <section class="ejh-signing wide" aria-label="EJH 双重验签">
  <strong>双重验签</strong>
  <p>新版 EJH 接口需要签名密钥。AppKey 和 AppSecret 继续保留。</p>
  <template v-if="settings.signing_key_present">
   <p class="configured">签名密钥已保存 · 私钥由服务器加密保管</p>
   <label class="field"><span>公钥（复制到 EJH 官网）</span><textarea readonly rows="4" :value="settings.signing_public_key" aria-label="EJH 公钥" spellcheck="false"/></label>
   <button type="button" class="btn ghost compact" @click="copy">复制公钥</button>
   <button type="button" class="btn ghost compact" :disabled="testing||busy" @click="regenerate">{{busy?'正在保存…':'重新生成密钥'}}</button>
   <button type="button" class="btn ghost compact" :disabled="testing||busy" @click="emit('test')">{{testing?'连接测试中…':'测试连接'}}</button>
   <p>在 EJH 官网「接口配置 → 配置公钥」中保存这段公钥，官网会重新生成 secretKey，旧值立即失效。请将新值填入本页 AppSecret，点击「保存配置」，再点击「测试连接」。</p>
  </template>
  <template v-else>
   <p>点击生成后，Space 自动保存私钥，并提供公钥供你填写到 EJH 官网。</p>
   <button type="button" class="btn ghost compact" :disabled="busy||!settings.secret_present||!settings.app_key" @click="save()">{{busy?'正在保存…':'生成签名密钥'}}</button>
   <small v-if="!settings.secret_present||!settings.app_key">请先保存 AppKey 和 AppSecret。</small>
   <details><summary>已有对应私钥</summary><label class="field"><span>RSA PKCS#8 私钥（PEM 或 Base64）</span><textarea v-model="privateKey" rows="4" autocomplete="off" spellcheck="false" aria-label="导入 EJH 签名私钥"/></label><button type="button" class="btn ghost compact" :disabled="busy||!privateKey.trim()||!settings.secret_present" @click="save(true)">加密保存私钥</button></details>
  </template>
  <p v-if="error" class="danger-text" role="alert">{{error}}</p><p v-if="notice" role="status">{{notice}}</p>
 </section>
</template>
<style scoped>
.ejh-signing{padding:16px;border:1px solid var(--line);border-radius:10px;background:var(--surface)}.ejh-signing p,.ejh-signing small{font-size:12px;line-height:1.7;color:var(--muted);margin:8px 0}.ejh-signing .configured{color:var(--green-strong)}.ejh-signing textarea{width:100%;font-family:monospace;overflow-wrap:anywhere;resize:vertical}.ejh-signing details{margin-top:12px}.ejh-signing summary{cursor:pointer;font-size:12px}
</style>
