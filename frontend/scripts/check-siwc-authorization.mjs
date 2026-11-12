import assert from 'node:assert/strict'
import vm from 'node:vm'
import {readFileSync} from 'node:fs'
import {computed} from 'vue'
const source=readFileSync(new URL('../src/components/ProAutoStages.vue',import.meta.url),'utf8')
const code=source.split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm,'')
for (const [mode,label] of [['siwc_local','SIWC 授权（本地）'],['siwc_sub2api','SIWC 授权（s2接口）'],['','RT/AT']]) {
 const props={state:{oauth_source:mode,steps:{oauth:'completed',push:'completed'}},disposition:'merge'}
 const ctx=vm.createContext({computed,defineProps:()=>props,defineEmits:()=>()=>{},accountDisposition:()=> 'merge'})
 vm.runInContext(code+';globalThis.stepsForTest=steps;',ctx)
 const rows=ctx.stepsForTest.value
 assert.equal(rows.find(([key])=>key==='oauth')[1],label)
 assert.equal(rows.find(([key])=>key==='push')[1],'推送')
 assert.equal(rows.length,6)
}
console.log('Passed: persisted SIWC authorization labels and independent push step')
