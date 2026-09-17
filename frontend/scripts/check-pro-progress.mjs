import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { parse } from '@vue/compiler-dom'
const source=readFileSync(new URL('../src/components/ProManagementView.vue',import.meta.url),'utf8')
const template=source.slice(source.indexOf('<template>')+'<template>'.length,source.lastIndexOf('</template>'))
const tree=parse(template)
function checkActionOrder(node) {
 const children=node.children || []
 const title=element=>element.props?.find(prop=>prop.type===6 && prop.name==='title')?.value?.content
 const login=children.findIndex(child=>title(child)==='登录获取 RT / AT')
 if (login>=0) {
  assert.equal(title(children[login+1]),'推送当前下游','OAuth login must immediately precede push')
  return true
 }
 return children.some(checkActionOrder)
}
assert.ok(checkActionOrder(tree),'Pro row must contain the OAuth login action')
const script=source.split('<script setup>')[1].split('</script>')[0]
const parsed=ts.createSourceFile('pro.js',script,ts.ScriptTarget.Latest,true,ts.ScriptKind.JS)
const code=parsed.statements.filter(node=>!ts.isImportDeclaration(node)).map(node=>node.getText(parsed)).join('\n')
function fixture() {
 const requests=[],timers=new Map()
 let timerID=0,unmount
 const document={hidden:false}
 const context=vm.createContext({
  defineProps:()=>({accounts:[],adminAccounts:[],defaultPageSize:10}),
  defineEmits:()=>()=>{},
  reactive:value=>value,ref:value=>({value}),computed:fn=>({get value(){return fn()}}),
  watch(){},onMounted(){},onBeforeUnmount(fn){unmount=fn},
  window:{setTimeout(fn,delay){const id=++timerID;timers.set(id,{fn,delay});return id},clearTimeout(id){timers.delete(id)},clearInterval(){}},
  api:async path=>new Promise((resolve,reject)=>requests.push({path,resolve,reject})),
  URLSearchParams,document,
 })
 vm.runInContext(code+';this.view={loadPage,schedulePoll,mergeStages,mergeActivity,runOne,message,rows,busy,page,activeTab}',context)
 const tick=async()=>{const [id,task]=[...timers][0];timers.delete(id);await task.fn()}
 return {...context.view,requests,timers,document,tick,unmount}
}
{
 const f=fixture()
 const states=f.mergeStages({pro_invite_status:'completed',pro_accept_status:'running',pro_transfer_status:'failed',pro_remove_status:'team_removed',invite_status:'failed'})
 assert.equal(states[0].status,'completed','Must read actual backend pro_* keys')
 assert.equal(states[1].text,'执行中')
 assert.equal(states[2].text,'失败')
 assert.equal(states[3].text,'已移出')
 assert.equal(f.mergeActivity({pro_accept_status:'running'}),'进入空间中')
 f.busy.value='merge:fixture@example.com'
 assert.equal(f.mergeActivity({email:'fixture@example.com'}),'正在启动或续跑')
}
{
 const f=fixture()
 const old=f.loadPage()
 f.page.value=2
 const current=f.loadPage()
 f.requests[1].resolve({items:[{email:'new@example.com'}],total:20})
 await current
 f.requests[0].resolve({items:[{email:'old@example.com'}],total:20})
 await old
 assert.equal(f.rows.value[0].email,'new@example.com','Old poll/page response overwrote current page')
}
{
 const f=fixture()
 const loading=f.loadPage()
 f.schedulePoll()
 await f.tick()
 assert.equal(f.requests.length,1,'Polling must not overlap a pending page load')
 f.requests[0].resolve({items:[{email:'fixture@example.com',pro_workflow_running:true}],total:1})
 await loading
 const polling=f.tick()
 f.requests[1].resolve({items:[{email:'fixture@example.com',pro_workflow_running:true}],total:1})
 await polling
 assert.equal([...f.timers.values()][0].delay,1500,'Running workflow should poll frequently')
 f.document.hidden=true
 await f.tick()
 assert.equal(f.requests.length,2,'Hidden document must not poll')
 f.document.hidden=false
 f.activeTab.value='push'
 await f.tick()
 assert.equal(f.requests.length,2,'Settings tab must not poll')
 f.unmount()
 assert.equal(f.timers.size,0,'Unmount must clear polling')
 f.schedulePoll()
 assert.equal(f.timers.size,0,'Unmount must not restart polling')
}
{
 const f=fixture()
 const loading=f.loadPage()
 f.unmount()
 f.requests[0].resolve({items:[{email:'late@example.com'}],total:1})
 await loading
 assert.equal(f.rows.value.length,0,'Unmount must discard late page results')
}
assert.ok(!source.includes('account.remove_status'),'Completed-action check must use pro_remove_status')
{
 const f=fixture()
 const account={email:'background@example.com',refresh_token_present:true}
 f.rows.value=[account]
 const running=f.runOne(account,'merge')
 assert.equal(f.busy.value,'merge:'+account.email)
 f.requests[0].resolve({...account,pro_workflow_running:true})
 await new Promise(resolve=>setImmediate(resolve))
 f.requests[1].resolve({items:[{...account,pro_workflow_running:true,pro_transfer_status:'running'}],total:1})
 await running
 assert.equal(f.busy.value,'','Accepted job must release page controls')
 assert.ok(f.message.text.includes('已提交后台'),'Receipt must not claim completion')
 assert.equal(f.rows.value[0].pro_workflow_running,true)
 const completed=f.loadPage()
 f.requests[2].resolve({items:[{...account,pro_workflow_running:false,pro_remove_status:'completed'}],total:1})
 await completed
 assert.ok(f.message.text.includes('四步流程完成'),'Polling must report actual completion')
}
{
 const f=fixture()
 const account={email:'uncertain@example.com',pro_workflow_running:true}
 f.rows.value=[account]
 const polling=f.loadPage()
 f.requests[0].resolve({items:[{...account,pro_workflow_running:false,pro_transfer_status:'unknown',pro_last_error:'合并结果待确认'}],total:1})
 await polling
 assert.equal(f.mergeStages(f.rows.value[0])[2].text,'待确认')
 assert.ok(f.message.text.includes('待确认'))
 assert.equal(f.message.type,'error')
}
{
 const f=fixture()
 const account={email:'receipt-lost@example.com'}
 f.rows.value=[account]
 const running=f.runOne(account,'merge')
 f.requests[0].reject(new Error('HTTP 502'))
 await new Promise(resolve=>setImmediate(resolve))
 f.requests[1].resolve({items:[{...account,pro_workflow_running:true}],total:1})
 await running
 assert.ok(f.message.text.includes('正在后台执行'),'Lost receipt must not overwrite persisted progress')
}
console.log('Pro stage mapping, immediate feedback, stale-response isolation, nonoverlapping polling, inactive tab and teardown passed')
