import assert from 'node:assert/strict';
import test from 'node:test';
import {mount, failureText, tracingText} from '../static/dashboard.mjs';

class Element {
 constructor(){this.nodeType=1;this.hidden=true;this.value='24h';this.textContent='';this.children=[];this.listeners={};}
 append(...children){this.children.push(...children);}
 replaceChildren(...children){this.children=children;}
 addEventListener(event,callback){this.listeners[event]=callback;}
}
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(){const nodes=new Map();return {visibilityState:'hidden',createElement:()=>new Element(),getElementById(id){if(!nodes.has(id))nodes.set(id,new Element());return nodes.get(id);}};}
const response=(status,data)=>({status,ok:status===200,json:async()=>data});
test('unknown error rate and configured export are distinguished from success',()=>{
 assert.equal(failureText(null),'—');assert.equal(failureText(0),'0.0%');
 assert.match(tracingText({configured:true,backend:'langfuse',exported_batches:0,failed_batches:1}),/已接收 0 批 \/ 失败 1 批/);
});
test('authentication uses only headers and health failure preserves overview',async()=>{
 const doc=fixture(),requests=[];
 const stop=mount(doc,async(path,options)=>{requests.push({path,options});if(!options.headers['X-Kepler-Agent-Admin-Token'])return response(403);return path.startsWith('/overview')?response(200,{runs:5,statuses:{error:1},recent_issues:[{id:'test',error:'<img onerror="alert(1)">'}]}):response(503);});
 try{
  await tick();assert.equal(doc.getElementById('auth').hidden,false);
  doc.getElementById('token').value='private-test-token';doc.getElementById('auth-form').listeners.submit({preventDefault(){}});await tick();await tick();
  assert.equal(doc.getElementById('token').value,'');assert.equal(doc.getElementById('runs').textContent,'5');assert.equal(doc.getElementById('auth').hidden,true);
  assert.ok(requests.every(r=>!r.path.includes('private-test-token')));assert.equal(requests.at(-1).options.headers['X-Kepler-Agent-Admin-Token'],'private-test-token');
  assert.match(doc.getElementById('health').children[0].textContent,/503/);
  const table=doc.getElementById('issues').children[0];const error=table.children[1].children[0].children[4].children[0];assert.equal(error.textContent,'<img onerror="alert(1)">');
 }finally{stop();}
});
test('a slow old window cannot replace a refreshed overview',async()=>{
 const doc=fixture();let resolveOld,overviews=0;
 const stop=mount(doc,async path=>{if(path.startsWith('/overview')){overviews++;if(overviews===1)return new Promise(resolve=>resolveOld=resolve);return response(200,{runs:9});}return response(503);});
 try{doc.getElementById('window').value='1h';doc.getElementById('window').listeners.change();await tick();resolveOld(response(200,{runs:1}));await tick();assert.equal(doc.getElementById('runs').textContent,'9');}finally{stop();}
});
