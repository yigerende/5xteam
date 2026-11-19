import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
const source=readFileSync(new URL('../src/components/ProManagementView.vue',import.meta.url),'utf8').split('<script setup>')[1].split('</script>')[0]
const parsed=ts.createSourceFile('view.js',source,ts.ScriptTarget.Latest,true,ts.ScriptKind.JS)
const names=new Set(['defaults','settings','pushModeDrafts','normalizeSection','applySettings','switchPushMode','syncGroupNames','connectionPayload','saveSettings'])
const declarations=parsed.statements.filter(s=>ts.isFunctionDeclaration(s)&&names.has(s.name?.text)||ts.isVariableStatement(s)&&s.declarationList.declarations.some(d=>names.has(d.name.getText(parsed)))).map(s=>s.getText(parsed)).join('\n')
const calls=[]
let fail=false,failSIWC=false,siwcSaved={seven_day_remaining:0,seven_day_enabled:true,poll_seconds:60,sub2:{proxy_release_minutes:0}}
const childSource=readFileSync(new URL('../src/components/ProSIWCSettings.vue',import.meta.url),'utf8').split('<script setup>')[1].split('</script>')[0]
const childParsed=ts.createSourceFile('child.js',childSource,ts.ScriptTarget.Latest,true,ts.ScriptKind.JS)
const childFunctions=childParsed.statements.filter(s=>ts.isFunctionDeclaration(s)&&['normalize','save','saveLocally'].includes(s.name?.text)).map(s=>s.getText(childParsed)).join('\n')
const child=vm.createContext({form:{value:structuredClone(siwcSaved)},busy:{value:false},message:{},api:async(path,options)=>{
 calls.push({path,options:structuredClone(options)})
 if(failSIWC)throw Error('SIWC save failed')
 siwcSaved=structuredClone(options.body);return structuredClone(siwcSaved)
}})
vm.runInContext(childFunctions+'\nthis.save=save;this.saveLocally=saveLocally',child)
const siwcSettings={value:{save:()=>child.save(),get busy(){return child.busy.value}}}
const ctx=vm.createContext({siwcSettings,reactive:v=>v,busy:{value:''},sub2Models:{value:''},sub2Groups:{value:[]},cpaGroups:{value:[]},groupErrors:{},setMessage:(text,type)=>{ctx.message={text,type}},updateCountdown:()=>{},api:async(path,options)=>{
 calls.push({path,options})
 if(path.includes('/variant'))return {auth_mode:'siwc',url:'',email:'',proxy_ids:null,account_concurrency:10}
 if(fail)throw Error('save failed')
 return {provider:'sub2',sub2:{...options.body.sub2,models:null,group_names:null},cpa:{},post_merge_group_ids:null}
}})
vm.runInContext(declarations+'\nthis.view={settings,switchPushMode,saveSettings,connectionPayload,applySettings}',ctx)
const v=ctx.view
v.settings.sub2.url='https://ordinary.test';v.settings.sub2.group_ids=[7];v.settings.sub2.proxy_ids=[9];v.settings.sub2_password='ordinary'
await v.switchPushMode({target:{value:'siwc'}})
assert.equal(v.settings.sub2.group_ids.length,0)
assert.equal(v.settings.sub2.proxy_ids.length,0)
v.settings.sub2.url='https://siwc.test';v.settings.sub2.email='admin@example.test';v.settings.sub2.group_ids=[2];v.settings.sub2_password='siwc'
assert.equal(v.connectionPayload('sub2').sub2.auth_mode,'siwc')
await v.switchPushMode({target:{value:''}})
assert.equal(v.settings.sub2.group_ids.join(','),'7')
assert.equal(v.settings.sub2.proxy_ids.join(','),'9')
assert.equal(v.settings.sub2_password,'ordinary')
await v.switchPushMode({target:{value:'siwc'}})
assert.equal(v.settings.sub2.group_ids.join(','),'2')
child.form.value.seven_day_remaining=1
fail=true
await v.saveSettings()
assert.equal(ctx.busy.value,'');assert.equal(ctx.message.type,'error');assert.equal(v.settings.sub2_password,'siwc')
fail=false
await v.saveSettings()
assert.equal(ctx.message.type,'success');assert.equal(ctx.busy.value,'')
assert.equal(siwcSaved.seven_day_remaining,1,'bottom Save must persist SIWC draft too')
child.form.value=structuredClone(siwcSaved)
assert.equal(child.form.value.seven_day_remaining,1,'reload uses stored threshold')
assert.equal(v.settings.sub2.models.length,0);assert.equal(v.settings.sub2.group_names.length,0);assert.equal(v.settings.post_merge_group_ids.length,0)
// A client-side preparation error must also release the save button.
ctx.sub2Models.value=null
await v.saveSettings()
assert.equal(ctx.busy.value,'');assert.equal(ctx.message.type,'error')
console.log('SIWC sparse configuration, switch drafts, connection mode and save recovery passed')

ctx.sub2Models.value='model'
failSIWC=true;child.form.value.seven_day_remaining=2
let count=calls.filter(r=>r.path==='/api/pro-settings').length
await v.saveSettings()
assert.equal(ctx.message.type,'error');assert.equal(ctx.message.text,'SIWC save failed')
assert.equal(calls.filter(r=>r.path==='/api/pro-settings').length,count,'SIWC failure must not be reported as successful parent save')
assert.equal(child.form.value.seven_day_remaining,2,'keep failed draft')
assert.equal(siwcSaved.seven_day_remaining,1,'failed save preserves stored value')
failSIWC=false;await child.saveLocally();assert.equal(siwcSaved.seven_day_remaining,2,'standalone button still saves')
child.busy.value=true;count=calls.length;await v.saveSettings();assert.equal(calls.length,count,'do not submit both buttons concurrently');child.busy.value=false
child.form.value=null;await v.saveSettings();assert.equal(ctx.message.type,'error');assert.match(ctx.message.text,/尚未加载/)
v.settings.sub2.auth_mode='';await v.saveSettings();assert.equal(ctx.message.type,'success','ordinary OAuth ignores absent SIWC panel')
const qualitySource=readFileSync(new URL('../src/components/ProQualityPanel.vue',import.meta.url),'utf8').split('<script setup>')[1].split('</script>')[0]
const qualityParsed=ts.createSourceFile('quality.js',qualitySource,ts.ScriptTarget.Latest,true,ts.ScriptKind.JS)
const qualitySave=qualityParsed.statements.find(s=>ts.isFunctionDeclaration(s)&&s.name.text==='save').getText(qualityParsed)
child.form.value=structuredClone(siwcSaved);child.form.value.seven_day_remaining=1
const quality=vm.createContext({siwcSettings,busy:{value:''},form:{action:'siwc'},data:{value:null},show:(text,type='error')=>{quality.message={text,type}},emit:()=>{},api:async(path,options)=>{calls.push({path,options});return {settings:{...options.body}}}})
vm.runInContext(qualitySave+'\nthis.save=save',quality)
await quality.save();assert.equal(siwcSaved.seven_day_remaining,1);assert.equal(quality.message.type,'success')
failSIWC=true;await quality.save();assert.equal(quality.message.type,'error')
console.log('Passed: both parent save buttons persist SIWC rule, reload 1%, independent save, failure propagation, draft preservation, busy/loading guards and ordinary OAuth isolation')
