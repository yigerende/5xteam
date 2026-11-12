export function globalProxyHealthText(value,now=Date.now()) {
 if(!value||value.state==='available')return '可用'
 if(value.state==='testing')return '恢复测试中'
 if(value.state==='recovering')return `等待下次测试 · 已测试 ${value.test_attempts||0} 次`
 const seconds=Math.max(0,Math.ceil((Number(value.cooldown_until)-now)/1000))
 return seconds>0?`冷却剩余 ${Math.floor(seconds/60)} 分 ${seconds%60} 秒`:'冷却结束，等待恢复'
}
