import assert from "node:assert/strict";
import test from "node:test";
import {mount,flush} from "./ui-harness.js";

function find(node:any,type:string):any {
 if(node?.type===type)return node;
 for(const child of node?.children??[]) {const found=find(child,type);if(found)return found;}
}

test("busy empty prompt stays focused so follow-ups can be typed",()=>{
 const ui=mount(new URL("../src/components/PromptInput.tsx",import.meta.url),"PromptInput",{value:"",busy:true,columns:80},{
  "../cc/kepler-ink.js":{Box:"Box",Text:"Text"},
  "string-width":{default:(text:string)=>text.length,__esModule:true},
  "../cc/components/KeplerTextInput.js":{KeplerTextInput:"Input"},
  "../lib/theme.js":{theme:{border:"white"}},
 });
 assert.equal(find(ui.current,"Input").props.focus,true);
});

test("draft is retained on rejection and newer typing survives an older acknowledgement",async()=>{
 let resolve:(accepted:boolean)=>void=()=>{};
 const ui=mount(new URL("../src/components/KeplerPromptFooter.tsx",import.meta.url),"KeplerPromptFooter",{busy:false,connecting:false,approval:null,onSubmitText:()=>new Promise<boolean>(r=>resolve=r)},{
  "../cc/kepler-ink.js":{Box:"Box"},"../cc/hooks/useTerminalSize.js":{useTerminalSize:()=>({columns:80})},
  "../lib/slashCommands.js":{filterSlashCommands:()=>[]},"./ApprovalPanel.js":{ApprovalPanel:"Approval"},"./PromptInput.js":{PromptInput:"Prompt"},"./SlashMenu.js":{SlashMenu:"Slash"},
 });
 find(ui.current,"Prompt").props.onChange("keep this");await flush();
 find(ui.current,"Prompt").props.onSubmit();resolve(false);await flush();
 assert.equal(find(ui.current,"Prompt").props.value,"keep this");
 find(ui.current,"Prompt").props.onSubmit();
 find(ui.current,"Prompt").props.onChange("new draft");await flush();
 resolve(true);await flush();
 assert.equal(find(ui.current,"Prompt").props.value,"new draft");
 find(ui.current,"Prompt").props.onSubmit();resolve(true);await flush();
 assert.equal(find(ui.current,"Prompt").props.value,"");
});
