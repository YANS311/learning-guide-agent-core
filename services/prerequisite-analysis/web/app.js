"use strict";
const $ = id => document.getElementById(id);
const names = {graph:"先修图谱",order:"学习顺序",compare:"依赖比较",missing:"缺失先修",impact:"依赖影响"};
let active = "", mode = "graph", lookup = new Map(), sequence = 0, searchSequence = 0;
const optionIDs = new Set();
const ns = "http://www.w3.org/2000/svg";
const message = {
  dependency_cycle:"当前依赖子图存在环路，无法生成合法学习顺序。",
  course_not_found:"没有找到该课程。请刷新课程列表后重试。",
  snapshot_unavailable:"暂时无法读取课程图谱。",
  query_timeout:"查询超时，请稍后重试。",
  invalid_request:"输入条件有误，请检查后重试。",
  snapshot_identity_unverified:"课程图谱数据源未通过身份校验。",
};
function el(tag,content,cls) {
  const node = document.createElement(tag);
  if (content !== undefined) node.textContent = content;
  if (cls) node.className = cls;
  return node;
}
function svg(tag, props) {
  const node = document.createElementNS(ns,tag);
  for (const [key,value] of Object.entries(props)) node.setAttribute(key,value);
  return node;
}
async function api(url,body) {
  const res=await fetch(url,body ? {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)} : {});
  const data=await res.json();
  if(!res.ok) {
    const code=data.error?.code;
    throw Error(message[code] || "暂时无法完成分析。");
  }
  $("version").textContent = "图谱版本 " + data.snapshot_version.slice(0,10);
  return data.data;
}
function error(text) {
  $("message").textContent = text;
  $("message").className = "error";
}
function label(id) {
  const c=lookup.get(id);
  return c ? c.name+" ("+id+")" : id;
}
function remember(courses) {
  for(const c of courses) lookup.set(c.id,c);
}
function table(title,heads,rows) {
  const section=document.createDocumentFragment();
  if(title) section.append(el("h2",title));
  const box=el("div",undefined,"table-scroll"), t=el("table"), th=el("thead"), tb=el("tbody"), top=el("tr");
  for(const h of heads) top.append(el("th",h));
  th.append(top);
  for(const row of rows) {
    const tr=el("tr");
    for(const item of row) tr.append(el("td",String(item)));
    tb.append(tr);
  }
  if(!rows.length) {
    const tr=el("tr"), td=el("td","无记录");
    td.colSpan=heads.length; tr.append(td); tb.append(tr);
  }
  t.append(th,tb); box.append(t); section.append(box); return section;
}
function summary(values) {
  const box=el("div",undefined,"summary");
  for(const [name,value] of values) {
    const cell=el("div");
    cell.append(el("strong",value),el("span"," "+name));
    box.append(cell);
  }
  return box;
}
function draw(result) {
  remember([result.course,...result.all_prerequisites]);
  const max=60;
  const shown=result.levels.slice(0,max).concat({course:result.course,column:0});
  const buckets=new Map();
  for(const item of shown) {
    if(!buckets.has(item.column)) buckets.set(item.column,[]);
    buckets.get(item.column).push(item.course);
  }
  const count=buckets.size;
  const rows=Math.max(...[...buckets.values()].map(bucket=>bucket.length));
  const height=Math.max(270,58*rows+50), width=Math.max(720,(count-1)*178+200);
  const points=new Map();
  [...buckets.keys()].sort((a,b)=>b-a).forEach((distance,column)=>{
    const list=buckets.get(distance);
    const x=count===1 ? width/2 : 100+column*(width-200)/(count-1);
    list.forEach((c,i)=>points.set(c.id,{x,y:height/2+(i-(list.length-1)/2)*58,c}));
  });
  const box=el("div",undefined,"graph-scroll");
  const chart=svg("svg",{width,height,viewBox:"0 0 "+width+" "+height,role:"group","aria-label":"先修关系有向图"});
  const defs=svg("defs",{}), marker=svg("marker",{id:"arrow",markerWidth:7,markerHeight:7,refX:6,refY:3.5,orient:"auto"});
  marker.append(svg("path",{d:"M0 0 L7 3.5 L0 7 Z",fill:"#557589"})); defs.append(marker); chart.append(defs);
  for(const r of result.relations) {
    const a=points.get(r.source),b=points.get(r.target);
    if(!a || !b) continue;
    const x=a.x+67, q=b.x-69;
    const edge=svg("path",{d:`M ${x},${a.y} C ${(x+q)/2},${a.y} ${(x+q)/2},${b.y} ${q},${b.y}`,
      fill:"none",stroke:r.strength==="weak"?"#967513":"#557589","stroke-width":1.4,
      "marker-end":"url(#arrow)","stroke-dasharray":r.strength==="weak"?"5 4":"none"});
    chart.append(edge);
  }
  for(const [id,{x,y,c}] of points) {
    const own=id===active;
    const group=svg("g",{class:"graph-node",transform:`translate(${x},${y})`,tabindex:0,role:"button","aria-label":"查看 "+c.name});
    group.append(svg("rect",{x:-68,y:-19,width:136,height:38,rx:6,fill:own?"#075f59":"#e9f4f2",stroke:own?"#075f59":"#9abdb6"}));
    const text=svg("text",{x:0,y:5,"text-anchor":"middle",fill:own?"#ffffff":"#174751","font-size":13});
    text.textContent=[...c.name].length>9?[...c.name].slice(0,9).join("")+"…":c.name;
    const tip=svg("title",{});tip.textContent=c.name+" "+c.id;
    group.append(text,tip);
    const go=()=>choose(id);
    group.addEventListener("click",go);
    group.addEventListener("keydown",e=>{if(e.key==="Enter"||e.key===" "){e.preventDefault();go();}});
    chart.append(group);
  }
  box.append(chart);
  return box;
}
function render(q) {
  const area=$("result");area.replaceChildren();
  if(mode==="graph"){
    area.append(summary([["门直接先修",q.direct.length],["门全部先修",q.all_prerequisites.length],["级依赖深度",q.depth]]),
      draw(q),el("p","箭头指向需要先修知识的课程。实线为必需先修，虚线为建议先修。","legend"));
    if(q.levels.length>60) area.append(el("p","图中展示最近的 60 门先修课程，完整列表见下表。","legend"));
    area.append(table("依赖层级",["距目标层数","课程编号","课程名称"],q.levels.map(x=>[x.distance,x.course.id,x.course.name])),
      table("代表依赖路径",["起点","路径"],q.paths.map(p=>[label(p.course_id),p.path.map(label).join(" → ")])));
  } else if(mode==="order"){
    remember(q.sequence);
    area.append(table("可行学习顺序",["顺序","学习阶段","课程"],q.sequence.map((c,i)=>[i+1,q.stages[i].stage,label(c.id)])));
  } else if(mode==="compare"){
    remember([...q.common,...q.left_only,...q.right_only]);
    area.append(summary([["左侧依赖深度",q.depths[0]],["右侧依赖深度",q.depths[1]],["条共同依赖边",q.structure.shared_edges],
      ["条左侧独有边",q.structure.left_only_edges],["条右侧独有边",q.structure.right_only_edges]]),
      table("依赖课程比较",["归属","课程"],[...q.common.map(c=>["共同先修",label(c.id)]),
        ...q.left_only.map(c=>["左侧独有",label(c.id)]),...q.right_only.map(c=>["右侧独有",label(c.id)])]),
      table("共同先修到两目标的路径",["共同先修","左侧路径","右侧路径"],q.common_paths.map(x=>[
        label(x.course_id),x.left.map(label).join(" → "),x.right.map(label).join(" → ")])));
  } else if(mode==="missing"){
    area.append(table("先修满足情况",["分类","课程"],[...q.satisfied_direct.map(id=>["已满足直接先修",label(id)]),
      ...q.missing_direct.map(id=>["未满足直接先修",label(id)]),...q.missing_indirect.map(id=>["未满足间接先修",label(id)])]));
  } else {
    remember([...q.direct,...q.indirect]);
    area.append(summary([["门直接受影响",q.direct.length],["门间接受影响",q.indirect.length],["门受影响课程",q.impact_count]]),
      table("后续依赖课程",["影响类型","课程"],[...q.direct.map(c=>["直接",label(c.id)]),...q.indirect.map(c=>["间接",label(c.id)])]),
      table("代表影响路径",["后续课程","路径"],q.paths.map(p=>[label(p.course_id),p.path.map(label).join(" → ")])));
  }
}
function choose(id){
  active=id;
  $("active-course").textContent=label(id);
  for(const b of $("catalog").querySelectorAll("button")) b.setAttribute("aria-pressed",String(b.dataset.id===id));
  run();
}
async function run() {
  if(!active) return;
  const current=++sequence, policy=$("policy").value;
  $("message").className=""; $("message").textContent="正在分析"; $("result").replaceChildren();
  const base="/api/v1/prerequisites";
  let url=`${base}/${encodeURIComponent(active)}?policy=${policy}`, body;
  if(mode==="impact") url=`${base}/${encodeURIComponent(active)}/dependents?policy=${policy}`;
  if(mode==="order") {url=base+"/learning-order";body={course_id:active,policy};}
  if(mode==="compare") {
    if(!$("other").value){$("message").textContent="请选择对比课程";return;}
    url=base+"/compare";body={left:active,right:$("other").value,policy};
  }
  if(mode==="missing"){
    url=base+"/missing";body={course_id:active,policy,completed:[...$("completed").querySelectorAll("input:checked")].map(x=>x.value)};
  }
  try{
    const data=await api(url,body);
    if(current!==sequence)return;
    $("message").textContent="";
    render(data);
  }catch(e){if(current===sequence)error(e.message);}
}
for(const b of document.querySelectorAll("[data-mode]"))b.addEventListener("click",()=>{
  mode=b.dataset.mode;
  for(const button of document.querySelectorAll("[data-mode]")){
    if(button===b)button.setAttribute("aria-current","page");else button.removeAttribute("aria-current");
  }
  $("title").textContent=names[mode];
  $("compare-form").hidden=mode!=="compare"; $("missing-form").hidden=mode!=="missing";
  run();
});
for(const form of ["compare-form","missing-form"])$(form).addEventListener("submit",e=>{e.preventDefault();run();});
$("policy").addEventListener("change",run);
async function search(keyword, initial=false) {
  const current=++searchSequence;
  try{
    const {items,total}=await api("/api/v1/courses?limit=100&q="+encodeURIComponent(keyword));
    if(current!==searchSequence)return;
    remember(items);
    $("search-count").textContent=`找到 ${total} 门课程${total>items.length?"，已显示前 "+items.length+" 门":""}`;
    $("catalog").replaceChildren();
    for(const c of items){
      const button=el("button");button.dataset.id=c.id;button.setAttribute("aria-pressed",String(c.id===active));
      button.append(el("span",c.name),el("small",c.id+" · "+c.semester));button.addEventListener("click",()=>choose(c.id));$("catalog").append(button);
    }
    if($("other").options.length===0) $("other").append(new Option("请选择课程","",true,true));
    for(const c of items) {
      if(optionIDs.has(c.id)) continue;
      optionIDs.add(c.id);
      $("other").append(new Option(label(c.id),c.id));
      const lab=el("label"), box=el("input");box.type="checkbox";box.value=c.id;
      lab.append(box,document.createTextNode(" "+c.name));$("completed").append(lab);
    }
    if((initial || !active) && items.length) choose(items.find(c=>c.id==="C401")?.id||items[0].id);
  }catch(e){if(current===searchSequence)error(e.message);}
}
$("search").addEventListener("submit",e=>{e.preventDefault();search($("keyword").value);});
search("",true);
