"use strict";
const by=id=>document.getElementById(id);
const form=by("profile"), request=by("request");
const keys={token:"course.advisor.token",run:"course.advisor.run",retry:"course.advisor.pending"};
const names={not_authenticated:"工作区未认证",not_found:"没有该记录",revision_conflict:"版本已更新，请刷新后再保存",
  queue_busy:"当前已有任务或队列已满",idempotency_conflict:"请求编号已用于不同输入",invalid_request:"请核对输入",
  unknown_course:"课程编号不在当前目录中",snapshot_changed:"课程快照已变化，请重新生成",
  prerequisites_unavailable:"先修分析服务暂不可用",catalog_too_large:"课程目录超出本次分析范围",
  run_timeout:"任务已超时",service_restarted:"服务重启，任务已中断，可重新发起",
  persistence_failed:"结果保存失败",internal_error:"服务暂时无法完成操作"};
const statusName={QUEUED:"等待执行",RUNNING:"正在分析",SUCCEEDED:"分析已完成",FAILED:"执行失败",CANCELLED:"已取消",
  INTERRUPTED:"已中断",REVIEW_REQUIRED:"方案待确认",UNKNOWN:"条件不足",NO_FEASIBLE_SELECTION:"本次未找到符合条件的课程"};
const stage={queued:"已接收",run_started:"开始执行",retrieval_completed:"证据检索完成",constraints_checked:"先修及约束已核对",
  plan_generated:"方案已生成",completed:"结果已保存",failed:"执行失败",cancelled:"已取消",interrupted:"已中断"};
const outcomes={SELECTED:"已纳入方案",ALREADY_COMPLETED:"已修课程",MISSING_REQUIRED:"缺少必需先修",
  CREDIT_LIMIT:"超过学分上限",UNKNOWN_CREDITS:"学分待确认",NOT_SELECTED_LIMIT:"超出推荐门数",
  NOT_CHECKED:"未纳入本次校验范围",USER_FILTER_MISMATCH:"不满足用户指定关键词"};
const warning={institutional_conditions_unverified:"开课、容量、时间冲突和正式选课资格未核验。",
  evidence_unavailable:"未匹配到可引用的培养方案片段。",candidate_budget_reached:"达到候选分析预算，未核验其余候选。",
  retrieval_unavailable:"证据服务暂不可用。",model_explanation_unavailable:"模型说明不可用，保留确定性分析结果。"};
let token=localStorage.getItem(keys.token), revision=0, catalog=[], completed=new Set(), parent="", active="", epoch=0, offset=0, flight=false, retrying=false;
function el(tag,text,cls){const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;}
function notice(message){by("notice").hidden=!message;by("notice").textContent=message;}
function error(e){notice(names[e.message]||"操作未完成，请检查连接后重试。");}
async function api(path,method="GET",body,key){
  const headers={};if(token)headers.Authorization="Bearer "+token;
  if(body!==undefined)headers["Content-Type"]="application/json";
  if(key)headers["Idempotency-Key"]=key;
  const res=await fetch(path,{method,headers,body:body===undefined?undefined:JSON.stringify(body)});
  const data=await res.json();
  if(!res.ok)throw Error(data.error);
  return data;
}
function updateCompleted(){
  const q=by("course-search").value.trim().toLowerCase(), options=by("completed");
  options.replaceChildren();
  for(const c of catalog){
    if(q&&!c.name.toLowerCase().includes(q)&&!c.id.toLowerCase().includes(q))continue;
    const label=el("label"), check=el("input");check.type="checkbox";check.value=c.id;check.checked=completed.has(c.id);
    check.addEventListener("change",()=>{if(check.checked)completed.add(c.id);else completed.delete(c.id);by("checked-count").textContent="("+completed.size+")";});
    label.append(check,el("span",c.name+" ("+c.id+")")); options.append(label);
  }
  by("checked-count").textContent="("+completed.size+")";
}
function fill(p){
  revision=p.revision;
  for(const name of ["name","stage","direction","preference"])form.elements[name].value=p[name]||"";
  completed=new Set(p.completed||[]);updateCompleted();
  by("revision").textContent="版本 "+revision;
}
function currentProfile(){return {revision,name:form.elements.name.value.trim(),stage:form.elements.stage.value.trim(),
  direction:form.elements.direction.value.trim(),preference:form.elements.preference.value.trim(),completed:[...completed].sort()};}
async function saveProfile(){
  const payload=currentProfile();
  if(!payload.name){form.reportValidity();throw Error("invalid_request");}
  fill(await api("/api/v1/profile","PUT",payload));
}
function table(heads,rows){
  const area=el("div",undefined,"table-wrap"), t=el("table"), th=el("thead"), tr=el("tr"), tb=el("tbody");
  for(const h of heads)tr.append(el("th",h));th.append(tr);
  if(!rows.length){const row=el("tr"),cell=el("td","无记录");cell.colSpan=heads.length;row.append(cell);tb.append(row);}
  for(const values of rows){const row=el("tr");for(const value of values){const td=el("td");td.append(value instanceof Node?value:document.createTextNode(String(value)));row.append(td);}tb.append(row);}
  t.append(th,tb);area.append(t);return area;
}
function tab(id){
  for(const b of document.querySelectorAll("[data-tab]")){if(b.dataset.tab===id)b.setAttribute("aria-current","page");else b.removeAttribute("aria-current");}
  for(const t of ["plan","history"])by(t).hidden=t!==id;
  if(id==="history")loadHistory();
}
function show(run){
  const area=by("result");area.replaceChildren();
  by("empty").hidden=true;
  by("state").textContent=statusName[run.plan?.status||run.state]||"";
  if(run.error)area.append(el("p",names[run.error]||"本次执行未完成。","warnings"));
  if(!run.plan)return;
  const p=run.plan;
  area.append(el("p","方案版本 "+run.version+"  ·  画像版本 "+p.profile_revision+"  ·  已核算 "+p.total_credits+" 学分","measure"));
  area.append(el("h2","建议课程"),table(["课程","课程编号","学分"],p.selected.map(c=>[c.name,c.id,c.credits])));
  const report=p.candidates.map(x=>[x.course.name,outcomes[x.outcome]||x.outcome,
    x.missing.map(id=>catalog.find(c=>c.id===id)?.name||id).join("、")||"—",x.score]);
  area.append(el("h2","约束校验"),table(["课程","校验结果","尚缺必需先修","偏好分"],report));
  area.append(el("h2","培养方案依据"));
  const refs=el("div",undefined,"references");
  for(const e of p.evidence){const row=el("div",undefined,"reference");row.append(el("strong",e.section),el("p",e.excerpt),
    el("small",e.source+" · "+e.doc_id+" · "+e.version+" · 匹配分 "+Number(e.score).toFixed(2)));refs.append(row);}
  if(!p.evidence.length)refs.append(el("p","本次未检索到匹配依据。","muted"));
  area.append(refs);
  if(p.explanation)area.append(el("p",p.explanation));
  area.append(el("p",p.warnings.map(x=>warning[x]||x).join(" "),"warnings"));
  area.append(el("small","快照 "+p.snapshot_version.slice(0,12)+" · 规则 "+p.rule_version));
}
function eventView(kind){
  if(stage[kind])by("timeline").append(el("li",stage[kind]));
}
async function listen(id,expected){
  let last=0;
  while(expected===epoch){
    try{
      const response=await fetch("/api/v1/runs/"+id+"/events",{headers:{Authorization:"Bearer "+token,"Last-Event-ID":String(last)}});
      if(!response.ok||!response.body)throw Error("internal_error");
      const reader=response.body.getReader(), decoder=new TextDecoder();let buffer="";
      try{
        while(expected===epoch){
          const {done,value}=await reader.read();if(done)break;
          buffer+=decoder.decode(value,{stream:true});
          let end;
          while((end=buffer.indexOf("\n\n"))>=0){
            const message=buffer.slice(0,end);buffer=buffer.slice(end+2);
            const n=Number(message.match(/^id: (\d+)$/m)?.[1]), kind=message.match(/^event: (.+)$/m)?.[1];
            if(n>last&&kind){last=n;eventView(kind);}
          }
        }
      }finally{await reader.cancel().catch(()=>{});}
      if(expected!==epoch)return;
      const run=await api("/api/v1/runs/"+id);
      show(run);
      if(!["QUEUED","RUNNING"].includes(run.state)){finish();return;}
    }catch(e){if(expected!==epoch)return;error(e);}
    await new Promise(resolve=>setTimeout(resolve,550));
  }
}
function track(run){
  active=run.id;localStorage.setItem(keys.run,active);++epoch;
  by("timeline").replaceChildren();show(run);flight=["QUEUED","RUNNING"].includes(run.state);
  by("cancel").hidden=!flight;by("generate").disabled=flight;
  listen(active,epoch);
}
function finish(){flight=false;by("cancel").hidden=true;by("generate").disabled=false;}
function useVersion(run){
  parent=run.id;
  request.elements.goal.value=run.request.goal;
  request.elements.limit.value=run.request.limit;
  request.elements.credits.value=run.request.max_credits??"";
  request.elements.keyword.value=run.request.required_keyword??"";
  request.elements.candidates.value=(run.request.course_ids||[]).join(", ");
  by("lineage").hidden=false;by("lineage").textContent="基于方案版本 "+run.version+" 重新规划";
  tab("plan");track(run);
}
async function loadHistory(){
  try{
    const rows=await api("/api/v1/runs?limit=10&offset="+offset);
    by("history-list").replaceChildren(table(["目标","版本","状态","操作"],rows.map(run=>{
      const open=el("button",run.plan?"查看并调整":"查看","secondary");open.type="button";
      open.addEventListener("click",()=>{if(run.plan)useVersion(run);else{tab("plan");track(run);}});
      return [run.goal,run.version,statusName[run.plan?.status||run.state]||run.state,open];
    })));
    by("prev").disabled=offset===0;by("next").disabled=rows.length<10;
  }catch(e){error(e);}
}
async function retryPending(saved){
  if(retrying)return;
  retrying=true;by("generate").disabled=true;
  try{
    const response=await api("/api/v1/runs","POST",saved.payload,saved.key);
    localStorage.removeItem(keys.retry);track(response);
  }catch(e){
    if(!(e instanceof TypeError))localStorage.removeItem(keys.retry);
    error(e);finish();
  }finally{retrying=false;}
}
form.addEventListener("submit",async e=>{e.preventDefault();notice("");try{await saveProfile();}catch(e){error(e);}});
request.addEventListener("submit",async e=>{
  e.preventDefault();if(flight||retrying)return;
  notice("");
  const prior=localStorage.getItem(keys.retry);
  if(prior){await retryPending(JSON.parse(prior));return;}
  if(!revision){notice("请先保存学生画像。");return;}
  const payload={profile_revision:revision,goal:request.elements.goal.value.trim(),
    course_ids:request.elements.candidates.value.split(/[,，\s]+/).filter(Boolean), limit:Number(request.elements.limit.value)};
  if(parent)payload.parent_run=parent;
  if(request.elements.credits.value)payload.max_credits=Number(request.elements.credits.value);
  if(request.elements.keyword.value)payload.required_keyword=request.elements.keyword.value.trim();
  const saved={payload,key:crypto.randomUUID()};
  localStorage.setItem(keys.retry,JSON.stringify(saved));
  await retryPending(saved);
});
by("cancel").addEventListener("click",async()=>{try{await api("/api/v1/runs/"+active+"/cancel","POST");}catch(e){error(e);}});
by("course-search").addEventListener("input",updateCompleted);
by("refresh").addEventListener("click",loadHistory);
by("prev").addEventListener("click",()=>{offset=Math.max(0,offset-10);loadHistory();});
by("next").addEventListener("click",()=>{offset+=10;loadHistory();});
for(const b of document.querySelectorAll("[data-tab]"))b.addEventListener("click",()=>tab(b.dataset.tab));
async function init(){
  try{
    if(!token){token=(await api("/api/v1/workspaces","POST",{})).token;localStorage.setItem(keys.token,token);}
    try{fill(await api("/api/v1/profile"));}catch(e){
      if(e.message==="not_authenticated"){for(const key of Object.values(keys))localStorage.removeItem(key);location.reload();return;}
      if(e.message!=="not_found")throw e;
    }
    try{const data=await api("/api/v1/courses");catalog=data.courses;updateCompleted();by("source").textContent="课程快照 "+data.version.slice(0,10);}
    catch(e){error(e);}
    const prior=localStorage.getItem(keys.retry);
    if(prior){await retryPending(JSON.parse(prior));return;}
    const old=localStorage.getItem(keys.run);
    if(old)track(await api("/api/v1/runs/"+old));
  }catch(e){error(e);}
}
init();
