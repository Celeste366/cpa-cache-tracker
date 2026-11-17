const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const script=fs.readFileSync(__dirname+'/panel.html','utf8').match(/<script>([\s\S]*?)<\/script>/)[1];

async function boot(key,status){
 let requests=0, intervals=0,ready;
 const modal={style:{}};
 const values=new Map(key ? [['cpa-cache-tracker-key',key]] : []);
 const storage={getItem:k=>values.get(k)||null,setItem:(k,v)=>values.set(k,v),removeItem:k=>values.delete(k)};
 const context={
  document:{documentElement:{},querySelectorAll:()=>[],getElementById:id=>id==='auth-modal-backdrop'?modal:null,addEventListener:(name,fn)=>{if(name==='DOMContentLoaded')ready=fn;}},
  window:{addEventListener(){},location:{host:'localhost:8080',search:'',pathname:'/',hash:''}},
  navigator:{userAgent:'test'},sessionStorage:storage,localStorage:{getItem:()=>null},URLSearchParams,
  fetch:async()=>{requests++;return {status,ok:status===200,json:async()=>({}),text:async()=>'{"error":"IP banned"}'};},
  console:{error(){},warn(){}},setTimeout,clearTimeout,setInterval:()=>{intervals++;return 1;},clearInterval:()=>{intervals=Math.max(0,intervals-1);}
 };
 vm.runInNewContext(script,context);
 ready();
 await new Promise(resolve=>setImmediate(resolve));
 return {requests,intervals,modal};
}

test('opening without a management key does not trigger authentication failures',async()=>{
 const result=await boot('',401);
 assert.equal(result.requests,0);
 assert.equal(result.intervals,0);
 assert.equal(result.modal.style.display,'flex');
});
for(const status of [401,403]) test(`HTTP ${status} stops polling until credentials are supplied`,async()=>{
 const result=await boot('invalid-key',status);
 assert.equal(result.requests,1);
 assert.equal(result.intervals,0);
 assert.equal(result.modal.style.display,'flex');
});
