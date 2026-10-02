import assert from "node:assert/strict";
import test from "node:test";
import { PassThrough } from "node:stream";
import * as protocol from "../src/client/appServer.js";
import { mount, flush } from "./ui-harness.js";

function pipes() {
  const stdin = new PassThrough(), stdout = new PassThrough();
  return {stdin, stdout, notify: (method:string, params:unknown) => stdout.write(JSON.stringify({jsonrpc:"2.0",method,params})+"\n")};
}
const quiet = {onDelta(){},onTurnStarted(){},onTurnCompleted(){},onApproval(){},onTool(){},onItem(){}};

test("disconnect rejects pending RPCs immediately and rejects new requests", async () => {
  const p = pipes();
  let disconnected = 0;
  const client = new protocol.AppServerClient(p.stdin,p.stdout,{...quiet,onDisconnect(){disconnected++;}});
  const first = assert.rejects(client.startThread(), /disconnected/);
  const second = assert.rejects(client.startTurn("s","hello"), /disconnected/);
  p.stdout.end();
  await Promise.all([first,second]);
  await assert.rejects(client.startThread(), /disconnected/);
  client.close();
  assert.equal(disconnected,1);
});

test("broken input pipe rejects RPC without an unhandled stream error", async () => {
  const p = pipes();
  const client = new protocol.AppServerClient(p.stdin,p.stdout,quiet);
  const pending = assert.rejects(client.startThread(), /broken pipe/);
  p.stdin.destroy(new Error("broken pipe"));
  await pending;
});

function repl() {
  const p = pipes();
  const starts: any[] = [];
  const respond = (request:any,result:unknown) => p.stdout.write(JSON.stringify({jsonrpc:"2.0",id:request.id,result})+"\n");
  const reject = (request:any) => p.stdout.write(JSON.stringify({jsonrpc:"2.0",id:request.id,error:{code:-32602,message:"rejected"}})+"\n");
  p.stdin.on("data",(data) => {
    for (const line of String(data).trim().split("\n")) {
      const request = JSON.parse(line);
      if (request.method === "initialize") respond(request,{protocol:"v2",minimumProtocolVersion:2,maximumProtocolVersion:2});
      if (request.method === "thread/start") respond(request,{sessionId:"s"});
      if (request.method === "turn/start") starts.push(request);
    }
  });
  const noop = () => {};
  const divider = {dividerIndex:0,dividerYRef:{current:0},onScrollAway:noop,onRepin:noop,jumpToNew:noop};
  const ui = mount(new URL("../src/hooks/useRepl.ts",import.meta.url),"useRepl",{cwd:"/workspace",model:"m",user:"u",resume:false,inputRouting:"queue"},{
    "../cc/kepler-ink.js":{useApp:()=>({exit:noop}),useInput:noop},
    "../cc/components/FullscreenLayout.js":{computeUnseenDivider:noop,useUnseenDivider:()=>divider},
    "../client/appServer.js":protocol,
    "../backend/spawn.js":{spawnBackend:()=>({...p,onExit:noop})},
    "../lib/toolDisplay.js":{toolDisplayName:(name:string)=>name},
    "../cc/utils/messages.js":{createUserMessage:({content}:any)=>({kind:"user",text:content}),createAssistantMessage:({content}:any)=>({kind:"assistant",text:content}),createSystemMessage:(text:string)=>({kind:"system",text})},
    "../cc/utils/messagePredicates.js":{isHumanTurn:(message:any)=>message.kind==="user"},
  });
  return {...p,ui,starts,respond,reject};
}

test("follow-ups reserve one start at a time, even before turn/started", async () => {
  const h = repl();
  await flush();
  assert.equal(h.ui.current.connectionState,"ready");
  const first = h.ui.current.submitText("first");
  assert.equal(await h.ui.current.submitText("second"),true);
  assert.equal(await h.ui.current.submitText("third"),true);
  await flush();
  assert.equal(h.starts.length,1);
  h.notify("turn/started",{turnId:"t1",sessionId:"s"});
  h.respond(h.starts[0],{turnId:"t1"});
  assert.equal(await first,true);
  h.notify("item/agentMessage/delta",{turnId:"t1",delta:"long intermediate commentary"});
  h.notify("turn/completed",{turnId:"t1",message:{content:[{type:"text",text:"done"}]}});
  await flush();
  assert.equal(h.starts.length,2);
  assert.equal(h.starts[1].params.input,"second");
  assert.equal(h.ui.current.messages.find((m:any)=>m.kind==="assistant").text,"done");
  await flush();
  assert.equal(h.starts.length,2);
  h.notify("turn/started",{turnId:"t2",sessionId:"s"});
  h.respond(h.starts[1],{turnId:"t2"});
  h.notify("turn/completed",{turnId:"t1"}); // stale completion cannot release t2
  await flush();
  assert.equal(h.starts.length,2);
  h.notify("turn/completed",{turnId:"t2"});
  await flush();
  assert.equal(h.starts.length,3);
  h.respond(h.starts[2],{turnId:"t3"});
  h.stdout.end();
  await flush();
  assert.equal(h.ui.current.busy,false);
  assert.equal(await h.ui.current.submitText("keep my draft"),false);
});

test("rejected start retains draft, removes optimistic message, and releases busy",async()=>{
  const h = repl();
  await flush();
  const accepted = h.ui.current.submitText("retry me");
  h.reject(h.starts[0]);
  assert.equal(await accepted,false);
  await flush();
  assert.equal(h.ui.current.busy,false);
  assert.equal(h.ui.current.messages.some((m:any)=>m.kind==="user"),false);
  h.stdout.end();
});

test("a rejected queued start pauses the queue and preserves its order",async()=>{
  const h = repl();
  await flush();
  const first = h.ui.current.submitText("first");
  await h.ui.current.submitText("second");
  await h.ui.current.submitText("third");
  h.notify("turn/started",{turnId:"t1",sessionId:"s"});
  h.respond(h.starts[0],{turnId:"t1"});await first;
  h.notify("turn/completed",{turnId:"t1"});await flush();
  h.reject(h.starts[1]);await flush();await flush();
  assert.equal(h.starts.length,2);
  assert.equal(await h.ui.current.submitText("fourth"),true);await flush();
  assert.equal(h.starts.length,3);
  assert.equal(h.starts[2].params.input,"second");
  h.respond(h.starts[2],{turnId:"t2"});h.stdout.end();await flush();
});
