import {api} from './api.js'

// Memory only, bounded and cleared whenever authentication changes. Never store
// account/financial details in localStorage or share them between logins.
export function createFinanceReportCache(request=api, now=Date.now) {
 const saved=new Map();let generation=0
 const key=params=>new URLSearchParams(Object.entries(params).filter(([,v])=>v!==''&&v!==undefined).sort(([a],[b])=>a.localeCompare(b))).toString()
 return {
  clear(){generation++;saved.clear()},
  peek(params){const item=saved.get(key(params));return item&&now()-item.at<300000?item.value:null},
  async read(params,signal){
   const query=key(params),epoch=generation
   const value=await request('/api/ejh/finance/report?'+query,{signal})
   if(epoch===generation&&!signal?.aborted){saved.delete(query);saved.set(query,{at:now(),value});while(saved.size>24)saved.delete(saved.keys().next().value)}
   return value
  },
 }
}
export const financeReportCache=createFinanceReportCache()
