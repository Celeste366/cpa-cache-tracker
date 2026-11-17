const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const script=fs.readFileSync(__dirname+'/panel.html','utf8').match(/<script>([\s\S]*?)<\/script>/)[1];

test('system appearance changes repaint the canvas using current CSS palette',()=>{
 let ready,themeListener,currentLabel='#6b7280',lastTextFill;
 const ctx={scale(){},clearRect(){},fillText(){lastTextFill=this.fillStyle;}};
 const canvas={getContext:()=>ctx,getBoundingClientRect:()=>({width:600,height:240}),addEventListener(){}};
 const context={
  document:{documentElement:{},querySelectorAll:()=>[],getElementById:id=>id==='trend-canvas'?canvas:null,addEventListener:(name,fn)=>{if(name==='DOMContentLoaded')ready=fn;}},
  window:{location:{search:"",pathname:"/",hash:""},devicePixelRatio:1,addEventListener(){},matchMedia:query=>{assert.equal(query,'(prefers-color-scheme: dark)');return {addEventListener:(name,fn)=>{assert.equal(name,'change');themeListener=fn;}};}},
  getComputedStyle:()=>({getPropertyValue:()=>currentLabel}),sessionStorage:{getItem:()=>null},localStorage:{getItem:()=>null},URLSearchParams,
  console,setTimeout,clearTimeout,setInterval,clearInterval
 };
 vm.runInNewContext(script,context);ready();
 assert.equal(typeof themeListener,'function','canvas must redraw immediately when the system appearance changes');
 themeListener();assert.equal(lastTextFill,currentLabel);
 currentLabel='#a4b6aa';themeListener();assert.equal(lastTextFill,currentLabel,'canvas still uses the previous light-mode color');
});
