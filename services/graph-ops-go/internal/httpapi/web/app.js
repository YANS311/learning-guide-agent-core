"use strict";
const $ = id => document.getElementById(id);
const keys = /^[A-Za-z0-9_-]{16,128}$/;
const savedName = "graphops operation";
let session = "", role = "", tab = "overview", target = null, plan = null, command = null, busy = false;
let memo = null, cursor = "", expires = null;
try {
  const raw = JSON.parse(sessionStorage.getItem(savedName));
  if (raw && keys.test(raw.key)) memo = {key:raw.key, unresolved:raw.unresolved === true};
} catch {}
const make = (tag, value, cls) => {
  const n = document.createElement(tag);
  if (value !== undefined) n.textContent = value;
  if (cls) n.className = cls;
  return n;
};
const date = stamp => new Date(stamp).toLocaleString();
function remember(value) {
  memo = value;
  try { if (value) sessionStorage.setItem(savedName, JSON.stringify(value)); else sessionStorage.removeItem(savedName); } catch {}
  if(value) $("operation-key").value = value.key;
  controls();
}
function result(id, title, lines = [], mood = "ok") {
  const box = make("div", undefined, "result " + mood);
  box.append(make("strong",title));
  for (const line of lines) box.append(make("p",line));
  $(id).replaceChildren(box);
}
function errors(body) {
  return (body.errors || []).map(x => [x.code,x.location,x.message].filter(Boolean).join(" · "));
}
async function api(path, opts = {}) {
  const h = new Headers();
  if (session) h.set("Authorization","Bearer "+session);
  if (opts.key) h.set("Idempotency-Key",opts.key);
  if (opts.data !== undefined) h.set("Content-Type", opts.type || "application/json");
  const res = await fetch(path,{method:opts.method || (opts.data === undefined ? "GET" : "POST"),
    headers:h,body:opts.data === undefined ? undefined : opts.type ? opts.data : JSON.stringify(opts.data),
    credentials:"omit",cache:"no-store"});
  const body = await res.json();
  if (res.status === 401 && opts.auth !== false) signedOut("Session expired. Sign in to continue.");
  return {status:res.status,body};
}
function signedOut(message = "") {
  session = "";
  role = "";
  if (!memo?.unresolved) command = null;
  target = null;
  plan = null;
  $("workspace").hidden = true;
  $("login-screen").hidden = false;
  $("login-error").textContent = message;
  $("access").value = "";
}
$("login-form").addEventListener("submit",async e => {
  e.preventDefault();
  $("login-error").textContent = "";
  try {
    const {status,body} = await api("/api/v1/admin/login",{data:{token:$("access").value},auth:false});
    $("access").value = "";
    if(status !== 200) { $("login-error").textContent = errors(body).join(" ") || "Sign-in failed."; return; }
    session = body.session; role = body.role; expires = body.expires_at;
    $("role").textContent = role;
    $("session-label").textContent = "Session expires " + date(expires);
    $("login-screen").hidden = true;
    $("workspace").hidden = false;
    for (const b of document.querySelectorAll("[data-tab]")) b.hidden = role === "observer" && b.dataset.tab === "import";
    $("preview-form").hidden = role === "observer";
    if (memo) $("operation-key").value = memo.key;
    if (memo?.unresolved) result("execution-result","Outcome not confirmed",["Check using the original operation key."],"pending");
    select("overview");
  } catch { $("access").value = ""; $("login-error").textContent = "Service unavailable."; }
});
$("logout").addEventListener("click", async () => { try { await api("/api/v1/admin/logout",{method:"POST"}); } catch {} signedOut(); });
function select(name) {
  tab = name;
  for (const b of document.querySelectorAll("[data-tab]")) {
    const active = b.dataset.tab === name;
    if(active) b.setAttribute("aria-current","page"); else b.removeAttribute("aria-current");
    $("panel-"+b.dataset.tab).hidden = !active;
  }
  $("notice").textContent = "";
  if(name === "overview" || name === "monitor") refresh();
  if(name === "history") history();
  controls();
}
for(const b of document.querySelectorAll("[data-tab]")) b.addEventListener("click",() => select(b.dataset.tab));
function metric(label,value,cls) {
  const d = make("div",undefined,"metric");
  d.append(make("small",label),make("strong",value,cls));
  return d;
}
function table(heads,rows) {
  const wrap = make("div",undefined,"table-wrap");
  const t = make("table"), h = make("thead"), b = make("tbody"), first = make("tr");
  for(const label of heads) first.append(make("th",label));
  h.append(first);
  for(const row of rows) {
    const tr = make("tr");
    for (const item of row) {
      const td = make("td");
      if(item instanceof Node) td.append(item); else td.textContent = String(item ?? "");
      tr.append(td);
    }
    b.append(tr);
  }
  t.append(h,b); wrap.append(t); return wrap;
}
async function refresh() {
  const dst = tab === "monitor" ? "monitor" : "overview";
  try {
    const {status,body:q}=await api("/api/v1/admin/monitoring");
    if(status !== 200) throw Error("Monitoring unavailable");
    $(dst+"-health").replaceChildren(
      metric("Go service","Live","good"),
      metric("Neo4j", q.neo4j_ready ? "Ready" : q.lab_configured ? "Unavailable" : "Unconfigured", q.neo4j_ready ? "good" : "warn"));
    const rows = [
      ["HTTP requests",q.http_requests],["Import validations",q.validations],["Rejected validations",q.validation_failures],
      ["Successful sync requests",q.sync_success],["Failed sync requests",q.sync_failures],
      ["Version conflicts",q.version_conflicts],["Idempotent replays",q.idempotency_replays],
      ["Confirmed rollbacks",q.confirmed_rollbacks],["Unknown commit results",q.commit_unknown],
      ["Measured transactions",q.transaction_count],["Average transaction latency",q.transaction_count ? q.average_transaction_ms.toFixed(1)+" ms" : "No samples"],
    ];
    if(dst === "overview") $("overview-stats").replaceChildren(...rows.slice(0,9).map(([k,v])=>metric(k,v)));
    else {
      $("metric-since").textContent = "Process counters since " + date(q.process_started_at);
      $("monitor-table").replaceChildren(table(["Metric","Observed value"],rows));
    }
    $("notice").textContent = "";
  } catch {
    $(dst+"-health").replaceChildren(metric("Service connection","Unavailable","warn"));
    $(dst === "overview" ? "overview-stats" : "monitor-table").replaceChildren();
    $("notice").textContent = "Live metrics unavailable.";
  }
}
for(const b of document.querySelectorAll("[data-refresh]")) b.addEventListener("click",refresh);
$("file").addEventListener("change", () => { target = null; plan = null; $("import-result").replaceChildren(); controls(); });
$("import-form").addEventListener("submit", async e => {
  e.preventDefault();
  const file = $("file").files[0];
  if(!file) return;
  target = null; plan = null;
  controls();
  try {
    const type = file.name.toLowerCase().endsWith(".csv") ? "text/csv" : "application/json";
    const {status,body} = await api("/api/v1/admin/import",{data:file,type});
    if (status!==200) { result("import-result","Import rejected",errors(body),"bad"); return; }
    target = body.data;
    if (!target.courses) target.courses = [];
    if (!target.relations) target.relations = [];
    $("scope").value = [...new Set(target.courses.map(c=>c.id).concat(target.relations.flatMap(x=>[x.source,x.target])))].join(", ");
    result("import-result","Validation passed",[`${body.courses} courses · ${body.relations} relations`]);
    const b=make("button","Open sync preview");
    b.addEventListener("click",()=>select("sync")); $("import-result").append(b);
  } catch { result("import-result","Import unavailable",[],"bad"); }
});
function controls() {
  const unresolved = memo?.unresolved === true;
  $("preview-btn").disabled = busy || !target || unresolved || role !== "operator";
  $("confirm").hidden = !plan || unresolved || role !== "operator";
  $("execute").disabled = busy || !plan || !$("confirmed").checked || unresolved;
  $("retry").hidden = !(unresolved && command && role === "operator");
  $("retry").disabled = busy;
}
$("scope").addEventListener("input",()=>{ plan=null; $("preview-result").replaceChildren(); controls(); });
$("confirmed").addEventListener("change",controls);
$("preview-form").addEventListener("submit",async e=>{
  e.preventDefault();
  if(!target || memo?.unresolved || busy) return;
  busy = true; plan = null; controls();
  $("confirmed").checked=false;
  $("execution-result").replaceChildren();
  try {
    const list=$("scope").value.split(/[\s,]+/).filter(Boolean);
    const {status,body}=await api("/api/v1/graph/db/preview",{data:{scope:{course_ids:list},target}});
    if(status!==200) { result("preview-result","Preview rejected",errors(body),"bad"); return; }
    plan=body;
    const p=body.plan;
    result("preview-result","Preview ready",[`Plan expires ${date(body.metadata.expires_at)}`]);
    const rows=[];
    const append=(kind,part,describe)=>{
      for(const v of part.added) rows.push(["Add",kind,describe(v), ""]);
      for(const v of part.modified) rows.push(["Modify",kind,describe(v.after), describe(v.before)]);
    };
    append("Course",p.courses,v=>`${v.id} · ${v.name} · credits ${v.credits ?? 0} · hours ${v.hours ?? 0} · ${v.semester ?? ""} ${v.category ?? ""} ${v.layer ?? ""} ${v.description ?? ""}`);
    append("PREREQ",p.relations,v=>`${v.source} → ${v.target} · ${v.strength ?? "strong"} ${v.reason ?? ""}`);
    $("preview-result").append(table(["Action","Type","Desired record","Previous record"],rows));
  } catch { result("preview-result","Preview unavailable",[],"bad"); }
  finally { busy=false;controls(); }
});
function opLines(o) {
  const c=o.statistics.courses,r=o.statistics.relations;
  return [`Operation ${o.operation_id}`,`Plan ${o.plan_id}`,
    `Courses +${c.added} / updated ${c.modified} · PREREQ +${r.added} / updated ${r.modified}`,
    `Started ${date(o.started_at)} · Finished ${date(o.finished_at)}`,`Request digest ${o.request_digest}`];
}
async function execute(replaying) {
  if (busy) return;
  if(!replaying) {
    if(!plan || !$("confirmed").checked || memo?.unresolved) return;
    command = {plan_token:plan.plan_token,target};
    remember({key:crypto.randomUUID(),unresolved:true});
  } else if(!command || !memo?.unresolved) return;
  busy=true;controls();
  const key=memo.key;
  result("execution-result","Submitting operation",["Keep the original idempotency key."],"pending");
  try {
    const {status,body}=await api("/api/v1/graph/db/execute",{key,data:command});
    if(status===200) {
      remember({key,unresolved:false}); command=null;plan=null;
      result("execution-result",body.replay ? "Committed operation recovered" : "Sync committed",opLines(body.operation));
    } else if(body.outcome === "rolled_back" || (!replaying && body.outcome === "rejected" && status>=400 && status<500)) {
      remember({key,unresolved:false}); command=null;plan=null;
      result("execution-result",body.outcome === "rolled_back" ? "Rollback confirmed" : "Sync rejected",errors(body),"bad");
    } else result("execution-result","Outcome not confirmed",errors(body).concat("Check using the original operation key."),"pending");
  } catch { result("execution-result","Outcome not confirmed",["The response was not received. Check using the original operation key."],"pending"); }
  finally { busy=false;controls(); }
}
$("execute").addEventListener("click",()=>execute(false));
$("retry").addEventListener("click",()=>execute(true));
$("recovery").addEventListener("submit",async e=>{
  e.preventDefault();
  const key=$("operation-key").value.trim();
  try {
    const {status,body}=await api("/api/v1/graph/db/operation",{key});
    if(status===200) {
      if(memo?.key===key) { remember({key,unresolved:false}); command=null;plan=null; controls(); }
      result("lookup-result","Committed record found",opLines(body));
    } else if(status===404) result("lookup-result","No committed record observed",["Check again with the same key."],"pending");
    else result("lookup-result","Outcome lookup unavailable",errors(body),"bad");
  } catch { result("lookup-result","Outcome lookup unavailable",[],"bad"); }
});
async function history(next = "") {
  $("detail-result").replaceChildren();
  try {
    const {status,body}=await api("/api/v1/admin/history?status=committed&limit=10&cursor="+encodeURIComponent(next));
    if(status!==200) { result("history-result","History unavailable",errors(body),"bad"); $("next").hidden=true; return; }
    cursor=body.next_cursor || "";
    $("next").hidden=!cursor;
    if(!body.items.length) { result("history-result","No committed operations"); return; }
    const rows=body.items.map(o=>{
      const b=make("button","View details");
      b.addEventListener("click",async()=>{
        try {
          const {status,body}=await api("/api/v1/admin/operation?id="+encodeURIComponent(o.operation_id));
          if(status!==200) { result("detail-result","Record unavailable",errors(body),"bad");return; }
          result("detail-result","Committed operation",opLines(body));
        } catch { result("detail-result","Record unavailable",[],"bad"); }
      });
      return [date(o.finished_at),o.operation_id.slice(0,12),o.status,o.statistics.courses.added,o.statistics.courses.modified,o.statistics.relations.added,o.statistics.relations.modified,b];
    });
    $("history-result").replaceChildren(table(["Finished","Operation","State","Courses +","Courses updated","PREREQ +","PREREQ updated",""],rows));
  } catch { result("history-result","History unavailable",[],"bad"); $("next").hidden=true; }
}
$("history-refresh").addEventListener("click",()=>history());
$("next").addEventListener("click",()=>history(cursor));
controls();
