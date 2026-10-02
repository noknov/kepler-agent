import assert from 'node:assert/strict';
import test from 'node:test';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const source = readFileSync(new URL('../static/app.js',import.meta.url),'utf8');
const between = (start,end) => source.slice(source.indexOf(start),source.indexOf(end,source.indexOf(start)));
function fixture() {
 const requests = [];
 const state = {conversations:[{id:'a'},{id:'b'}],events:[],current:null};
 const errors = [];
 const context = vm.createContext({state,AbortController,encodeURIComponent,
  api:(path,{signal})=>new Promise((resolve,reject)=>requests.push({path,signal,resolve,reject})),
  normalizeEvents:events=>events,
  closeStream(){},clearTimeline(){},renderConversations(){},updateComposer(){},renderTimeline(){},openStream(){},closeSidebar(){},clearPendingThinking(){},toast:message=>errors.push(message),
 });
 vm.runInContext(between('// A snapshot belongs','function showEmpty()')+between('function deriveRunningFromEvents','function ensureStream()')+'\nglobalThis.apiTest = {selectConversation,syncConversationMessages,liveEvent(){liveRevision++;}};',context);
 return {...context.apiTest,state,requests,errors};
}

test('late conversation response cannot replace current conversation',async()=>{
 const f=fixture();
 const a=f.selectConversation('a');
 const b=f.selectConversation('b');
 assert.equal(f.requests[0].signal.aborted,true);
 f.requests[1].resolve({events:[{id:'b-event',sequence:2}]}); await b;
 f.requests[0].resolve({events:[{id:'a-event',sequence:99}]}); await a;
 assert.equal(f.state.events[0].id,'b-event');
 assert.equal(f.state.maxSequence,2);
});

test('returning to same conversation still rejects an earlier request',async()=>{
 const f=fixture();
 const first=f.selectConversation('a'), middle=f.selectConversation('b'), last=f.selectConversation('a');
 f.requests[2].resolve({events:[{id:'latest'}]}); await last;
 f.requests[0].resolve({events:[{id:'old'}]}); await first;
 f.requests[1].reject(new Error('aborted')); await middle;
 assert.equal(f.state.events[0].id,'latest');
 assert.equal(f.errors.length,0);
});

test('background recovery cannot overwrite a newer live event or another selection',async()=>{
 const f=fixture();
 const selected=f.selectConversation('a');f.requests[0].resolve({events:[]});await selected;
 const sync=f.syncConversationMessages();
 f.state.events.push({id:'live'});f.liveEvent();
 f.requests[1].resolve({events:[]});await sync;
 assert.equal(f.state.events[0].id,'live');
 const stale=f.syncConversationMessages();
 const switched=f.selectConversation('b');
 f.requests[3].resolve({events:[{id:'b'}]});await switched;
 f.requests[2].resolve({events:[{id:'a'}]});await stale;
 assert.equal(f.state.events[0].id,'b');
});
