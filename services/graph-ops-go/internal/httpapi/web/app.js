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
const date = stamp => new Date(stamp).toLocaleString("zh-CN",{hour12:false});
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
const errorText = {
  login_rejected:"访问令牌无效或无法创建会话",
  authentication_required:"会话无效，请重新登录",
  permission_denied:"当前账号没有操作权限",
  invalid_csv_header:"CSV 表头与字段规范不符",
  invalid_csv:"CSV 记录格式不正确",
  invalid_json:"JSON 格式不正确或包含不支持的字段",
  invalid_type:"字段类型不符合要求",
  required_field:"缺少必填字段",
  invalid_value:"字段取值不符合要求",
  duplicate_course:"存在重复课程编号",
  duplicate_relation:"存在重复先修关系",
  duplicate_name:"存在重复课程名称",
  unsupported_relation:"仅支持课程先修关系",
  invalid_reference:"引用的课程不存在",
  prerequisite_cycle:"先修关系存在环路",
  invalid_scope:"管理范围为空、重复或格式无效",
  out_of_scope:"课程或关系不在指定范围内",
  outside_managed_scope:"数据超出实验库允许管理的范围",
  target_and_scope_required:"请提供导入数据及管理范围",
  invalid_graph_plan:"同步计划未通过校验",
  plan_expired:"计划已过期，请重新生成预览",
  database_state_changed:"数据库已有变更，请重新生成预览",
  idempotency_conflict:"该操作查询键已对应另一条请求",
  plan_target_mismatch:"提交数据与预览数据不一致",
  commit_outcome_unknown:"提交回执无法确认，请用原查询键核对结果",
  operation_not_observed:"暂未查到已提交记录",
  neo4j_unavailable:"暂时无法连接实验图数据库",
  lab_not_configured:"尚未配置合成实验数据库",
  invalid_idempotency_key:"操作查询键格式无效",
  empty_input:"文件为空",no_records:"没有可校验的记录",body_limit:"文件超过大小限制",
  record_limit:"记录数超过限制",request_timeout:"请求超时",
};
function errors(body) {
  return (body.errors || []).map(x => {
    const detail = errorText[x.code] || "操作未完成";
    return detail + "（" + [x.code,x.location].filter(Boolean).join(" · ") + "）";
  });
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
  if (res.status === 401 && opts.auth !== false) signedOut("会话已过期，请重新登录。");
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
    if(status !== 200) { $("login-error").textContent = errors(body).join(" ") || "登录失败。"; return; }
    session = body.session; role = body.role; expires = body.expires_at;
    $("role").textContent = role === "observer" ? "只读人员" : "管理员";
    $("session-label").textContent = "会话到期：" + date(expires);
    $("login-screen").hidden = true;
    $("workspace").hidden = false;
    for (const b of document.querySelectorAll("[data-tab]")) b.hidden = role === "observer" && b.dataset.tab === "import";
    $("preview-form").hidden = role === "observer";
    if (memo) $("operation-key").value = memo.key;
    if (memo?.unresolved) result("execution-result","提交结果待确认",["请使用原操作查询键核对结果。"],"pending");
    select("overview");
  } catch { $("access").value = ""; $("login-error").textContent = "服务暂不可用。"; }
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
    if(status !== 200) throw Error("监测数据暂不可用");
    $(dst+"-health").replaceChildren(
      metric("Go 服务","运行中","good"),
      metric("Neo4j", q.neo4j_ready ? "已就绪" : q.lab_configured ? "不可用" : "未配置", q.neo4j_ready ? "good" : "warn"));
    const rows = [
      ["HTTP 请求",q.http_requests],["导入校验",q.validations],["校验未通过",q.validation_failures],
      ["同步成功请求",q.sync_success],["同步失败请求",q.sync_failures],
      ["版本冲突",q.version_conflicts],["幂等重放",q.idempotency_replays],
      ["已确认回滚",q.confirmed_rollbacks],["提交结果未知",q.commit_unknown],
      ["已测量事务",q.transaction_count],["平均事务耗时",q.transaction_count ? q.average_transaction_ms.toFixed(1)+" ms" : "暂无样本"],
    ];
    if(dst === "overview") $("overview-stats").replaceChildren(...rows.slice(0,9).map(([k,v])=>metric(k,v)));
    else {
      $("metric-since").textContent = "本次服务启动时间：" + date(q.process_started_at);
      $("monitor-table").replaceChildren(table(["监测指标","观测值"],rows));
    }
    $("notice").textContent = "";
  } catch {
    $(dst+"-health").replaceChildren(metric("服务连接","不可用","warn"));
    $(dst === "overview" ? "overview-stats" : "monitor-table").replaceChildren();
    $("notice").textContent = "暂时无法获取实时指标。";
  }
}
for(const b of document.querySelectorAll("[data-refresh]")) b.addEventListener("click",refresh);
$("file").addEventListener("change", () => {
  target = null; plan = null;
  $("file-name").textContent = $("file").files[0]?.name || "尚未选择文件";
  $("import-result").replaceChildren(); controls();
});
$("import-form").addEventListener("submit", async e => {
  e.preventDefault();
  const file = $("file").files[0];
  if(!file) return;
  target = null; plan = null;
  controls();
  try {
    const type = file.name.toLowerCase().endsWith(".csv") ? "text/csv" : "application/json";
    const {status,body} = await api("/api/v1/admin/import",{data:file,type});
    if (status!==200) { result("import-result","导入校验未通过",errors(body),"bad"); return; }
    target = body.data;
    if (!target.courses) target.courses = [];
    if (!target.relations) target.relations = [];
    $("scope").value = [...new Set(target.courses.map(c=>c.id).concat(target.relations.flatMap(x=>[x.source,x.target])))].join(", ");
    result("import-result","数据校验通过",[`${body.courses} 门课程 · ${body.relations} 条先修关系`]);
    const rows = target.courses.slice(0,8).map(c=>[c.id,c.name,c.credits ?? 0,c.hours ?? 0]);
    if(rows.length) $("import-result").append(table(["课程编号","课程名称","学分","学时"],rows));
    const b=make("button","前往同步预览");
    const actions=make("div",undefined,"actions");
    b.addEventListener("click",()=>select("sync")); actions.append(b); $("import-result").append(actions);
  } catch { result("import-result","暂时无法校验文件",[],"bad"); }
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
    if(status!==200) { result("preview-result","预览未通过",errors(body),"bad"); return; }
    plan=body;
    const p=body.plan;
    result("preview-result","同步计划已生成",[`计划有效期至 ${date(body.metadata.expires_at)}`]);
    const rows=[];
    const append=(kind,part,describe)=>{
      for(const v of part.added) rows.push(["新增",kind,describe(v), ""]);
      for(const v of part.modified) rows.push(["修改",kind,describe(v.after), describe(v.before)]);
    };
    append("课程",p.courses,v=>[v.id,v.name,`学分 ${v.credits ?? 0}`,`学时 ${v.hours ?? 0}`,v.semester,v.category,v.layer,v.description].filter(Boolean).join(" · "));
    append("先修关系",p.relations,v=>[`${v.source} → ${v.target}`,v.strength === "weak" ? "建议先修" : "必需先修",v.reason].filter(Boolean).join(" · "));
    $("preview-result").append(table(["操作","对象","变更后","变更前"],rows));
  } catch { result("preview-result","暂时无法生成预览",[],"bad"); }
  finally { busy=false;controls(); }
});
function opLines(o) {
  const c=o.statistics.courses,r=o.statistics.relations;
  return [`操作编号 ${o.operation_id}`,`计划编号 ${o.plan_id}`,
    `课程新增 ${c.added}、修改 ${c.modified} · 先修关系新增 ${r.added}、修改 ${r.modified}`,
    `开始 ${date(o.started_at)} · 完成 ${date(o.finished_at)}`,`请求摘要 ${o.request_digest}`];
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
  result("execution-result","正在提交同步",["请保留本次操作的查询键。"],"pending");
  try {
    const {status,body}=await api("/api/v1/graph/db/execute",{key,data:command});
    if(status===200) {
      remember({key,unresolved:false}); command=null;plan=null;
      result("execution-result",body.replay ? "已恢复原同步记录" : "同步已提交",opLines(body.operation));
    } else if(body.outcome === "rolled_back" || (!replaying && body.outcome === "rejected" && status>=400 && status<500)) {
      remember({key,unresolved:false}); command=null;plan=null;
      result("execution-result",body.outcome === "rolled_back" ? "已确认回滚" : "同步已拒绝",errors(body),"bad");
    } else result("execution-result","提交结果待确认",errors(body).concat("请使用原操作查询键核对结果。"),"pending");
  } catch { result("execution-result","提交结果待确认",["未收到执行回执，请用原查询键核对结果。"],"pending"); }
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
      result("lookup-result","已查到提交记录",opLines(body));
    } else if(status===404) result("lookup-result","暂未查到已提交记录",["可用同一查询键稍后复查。"],"pending");
    else result("lookup-result","暂时无法查询结果",errors(body),"bad");
  } catch { result("lookup-result","暂时无法查询结果",[],"bad"); }
});
async function history(next = "") {
  $("detail-result").replaceChildren();
  try {
    const {status,body}=await api("/api/v1/admin/history?status=committed&limit=10&cursor="+encodeURIComponent(next));
    if(status!==200) { result("history-result","暂时无法加载操作历史",errors(body),"bad"); $("next").hidden=true; return; }
    cursor=body.next_cursor || "";
    $("next").hidden=!cursor;
    if(!body.items.length) { result("history-result","暂无已提交操作"); return; }
    const rows=body.items.map(o=>{
      const b=make("button","查看详情");
      b.addEventListener("click",async()=>{
        try {
          const {status,body}=await api("/api/v1/admin/operation?id="+encodeURIComponent(o.operation_id));
          if(status!==200) { result("detail-result","暂时无法读取操作详情",errors(body),"bad");return; }
          result("detail-result","已提交操作",opLines(body));
        } catch { result("detail-result","暂时无法读取操作详情",[],"bad"); }
      });
      return [date(o.finished_at),o.operation_id.slice(0,12),o.status === "committed" ? "已提交" : o.status,o.statistics.courses.added,o.statistics.courses.modified,o.statistics.relations.added,o.statistics.relations.modified,b];
    });
    $("history-result").replaceChildren(table(["完成时间","操作编号","状态","课程新增","课程修改","关系新增","关系修改","详情"],rows));
  } catch { result("history-result","暂时无法加载操作历史",[],"bad"); $("next").hidden=true; }
}
$("history-refresh").addEventListener("click",()=>history());
$("next").addEventListener("click",()=>history(cursor));
controls();
