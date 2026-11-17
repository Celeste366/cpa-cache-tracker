const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const script=fs.readFileSync(__dirname+'/panel.html','utf8').match(/<script>([\s\S]*?)<\/script>/)[1];

test('language selector translates the page and saves preference for the next session',()=>{
 let ready,change;const values=new Map();
 const storage={getItem:k=>values.get(k)||null,setItem:(k,v)=>values.set(k,v)};
 const label={dataset:{i18n:'缓存概览'},textContent:'缓存概览'};
 const selector={value:'zh',addEventListener:(name,fn)=>{if(name==='change')change=fn;}};
 const document={documentElement:{lang:'zh-CN'},querySelectorAll:q=>q==='[data-i18n]'?[label]:[],getElementById:id=>id==='language-select'?selector:null,addEventListener:(name,fn)=>{if(name==='DOMContentLoaded')ready=fn;}};
 const context={document,window:{location:{search:'',pathname:'/',hash:''},addEventListener(){}},sessionStorage:{getItem:()=>null},localStorage:storage,URLSearchParams,console,setTimeout,clearTimeout,setInterval,clearInterval};
 vm.runInNewContext(script.replace("// Initialize", "globalThis.__testState = state; // Initialize"),context);ready();
 context.__testState.summary={totals:{},models:[],miss_reasons:[]};
 assert.equal(typeof change,'function','language selector must be usable');
 selector.value='en';change();assert.equal(label.textContent,'Cache overview');assert.equal(document.documentElement.lang,'en');
 assert.equal(storage.getItem('cpa-cache-tracker-language'),'en');
 label.textContent='缓存概览';
 vm.runInNewContext(script.replace("// Initialize", "globalThis.__testState = state; // Initialize"),context);ready();
 assert.equal(label.textContent,'Cache overview','saved preference should survive a reload');
 selector.value='zh';change();assert.equal(label.textContent,'缓存概览');assert.equal(document.documentElement.lang,'zh-CN');
});
