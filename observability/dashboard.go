package observabilitysvc

const healthDashboardHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>斗包 Tool Health</title>
  <style>
    :root { color-scheme: light dark; --bg:#f7f8fa; --fg:#182026; --muted:#68737d; --line:#d9dee4; --panel:#fff; --healthy:#087443; --degraded:#9a5b00; --unhealthy:#b42318; --unknown:#59636e; --accent:#2563eb; }
    @media (prefers-color-scheme: dark) { :root { --bg:#111418; --fg:#edf1f5; --muted:#a1abb5; --line:#303842; --panel:#181d23; --healthy:#34d399; --degraded:#fbbf24; --unhealthy:#fb7185; --unknown:#94a3b8; --accent:#60a5fa; } }
    * { box-sizing: border-box; }
    body { margin:0; background:var(--bg); color:var(--fg); font:14px/1.45 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
    header, main { max-width:1180px; margin:0 auto; padding:18px 20px; }
    header { display:flex; align-items:center; justify-content:space-between; gap:16px; border-bottom:1px solid var(--line); }
    h1 { margin:0; font-size:20px; letter-spacing:0; }
    h2 { margin:0 0 10px; font-size:15px; letter-spacing:0; }
    button { border:1px solid var(--line); border-radius:6px; background:var(--panel); color:var(--fg); cursor:pointer; padding:8px 12px; font:inherit; }
    button:hover { border-color:var(--accent); color:var(--accent); }
    .summary { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:12px; margin-bottom:16px; }
    .metric, section { background:var(--panel); border:1px solid var(--line); border-radius:8px; padding:14px; }
    .metric strong { display:block; font-size:22px; margin-top:4px; }
    .muted { color:var(--muted); }
    .grid { display:grid; grid-template-columns:minmax(0,1fr) minmax(340px,.85fr); gap:16px; align-items:start; }
    table { width:100%; border-collapse:collapse; }
    th, td { border-top:1px solid var(--line); padding:9px 8px; text-align:left; vertical-align:top; }
    th { color:var(--muted); font-weight:600; font-size:12px; }
    code { background:color-mix(in srgb,var(--muted) 14%,transparent); border-radius:4px; padding:2px 4px; word-break:break-word; }
    .status { display:inline-flex; align-items:center; gap:6px; font-weight:700; text-transform:capitalize; }
    .dot { width:8px; height:8px; border-radius:999px; background:var(--unknown); }
    .healthy { color:var(--healthy); } .healthy .dot { background:var(--healthy); }
    .degraded { color:var(--degraded); } .degraded .dot { background:var(--degraded); }
    .unhealthy { color:var(--unhealthy); } .unhealthy .dot { background:var(--unhealthy); }
    .unknown { color:var(--unknown); } .unknown .dot { background:var(--unknown); }
    .error { color:var(--unhealthy); white-space:pre-wrap; }
    .stack { display:grid; gap:16px; }
    @media (max-width:860px) { header { align-items:flex-start; flex-direction:column; } .summary, .grid { grid-template-columns:1fr; } }
  </style>
</head>
<body>
  <header><div><h1>斗包 Tool Health</h1><div class="muted" id="checked">Loading...</div></div><button id="refresh" type="button">Refresh</button></header>
  <main>
    <div class="summary"><div class="metric"><span class="muted">Overall</span><strong id="overall">-</strong></div><div class="metric"><span class="muted">Tools</span><strong id="toolCounts">-</strong></div></div>
    <div class="grid"><section><h2>Tools</h2><table><thead><tr><th>Name</th><th>Status</th><th>Message</th><th>Latency</th></tr></thead><tbody id="tools"></tbody></table></section><div class="stack"><section><h2>Recent Errors</h2><div id="errors" class="muted">-</div></section></div></div>
  </main>
  <script>
    const tokenKey="slackCopilotAgentAdminToken";
    const esc=value=>String(value??"").replace(/[&<>"']/g,ch=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[ch]));
    const statusHTML=status=>'<span class="status '+esc(status||"unknown")+'"><span class="dot"></span>'+esc(status||"unknown")+'</span>';
    const fmtDate=value=>value?new Date(value).toLocaleString():"-";
    const headers=()=>{const token=localStorage.getItem(tokenKey)||""; return token?{"X-Slack-Copilot-Agent-Admin-Token":token}:{};};
    async function getJSON(path){const res=await fetch(path,{headers:headers()}); if(res.status===403){const token=prompt("Admin token"); if(token){localStorage.setItem(tokenKey,token); return getJSON(path);}} if(!res.ok) throw new Error(path+" returned "+res.status); return res.json();}
    function render(snapshot,metrics){const tools=snapshot.tools||[]; const counts=tools.reduce((acc,tool)=>{acc[tool.status||"unknown"]=(acc[tool.status||"unknown"]||0)+1; return acc;},{}); document.getElementById("checked").textContent="Checked "+fmtDate(snapshot.checked_at); document.getElementById("overall").innerHTML=statusHTML(snapshot.overall); document.getElementById("toolCounts").textContent=tools.length+" total, "+(counts.healthy||0)+" healthy"; document.getElementById("tools").innerHTML=tools.map(tool=>'<tr><td><code>'+esc(tool.name)+'</code><div class="muted">'+esc(tool.criticality||"")+'</div></td><td>'+statusHTML(tool.status)+'</td><td>'+esc(tool.message||"-")+'</td><td>'+esc(tool.latency_ms||0)+' ms</td></tr>').join(""); const errs=(metrics&&metrics.last_errors)||[]; document.getElementById("errors").innerHTML=errs.length?errs.map(err=>'<p class="error">'+esc(err)+'</p>').join(""):'<span class="muted">No recent errors</span>';}
    async function load(){document.getElementById("refresh").disabled=true; try{const [snapshot,metrics]=await Promise.all([getJSON("/health/tools?refresh=true"),getJSON("/metrics")]); render(snapshot,metrics);} catch(err){document.getElementById("checked").textContent=err.message;} finally{document.getElementById("refresh").disabled=false;}}
    document.getElementById("refresh").addEventListener("click",load); load();
  </script>
</body>
</html>`
