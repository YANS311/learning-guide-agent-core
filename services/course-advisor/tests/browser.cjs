const assert=require("node:assert/strict");
const {spawn}=require("node:child_process");
const {once}=require("node:events");
const fs=require("node:fs/promises");
const path=require("node:path");
const os=require("node:os");
const net=require("node:net");
const {chromium}=require("playwright");

async function port(){
  const s=net.createServer();
  s.listen(0,"127.0.0.1");await once(s,"listening");
  const value=s.address().port;
  await new Promise(resolve=>s.close(resolve));
  return value;
}

function processOf(command,args,cwd){
  const child=spawn(command,args,{cwd,stdio:["ignore","ignore","inherit"],windowsHide:true});
  const exited=once(child,"exit");
  return {async stop(){if(child.exitCode===null&&child.signalCode===null)child.kill();await exited;},child};
}

async function checkReady(url,child){
  for(let attempt=0;attempt<60;attempt++){
    if(child?.exitCode!==null&&child?.exitCode!==undefined)break;
    try{const r=await fetch(url+"/health/ready");if(r.ok)return r.json();}catch{}
    await new Promise(resolve=>setTimeout(resolve,250));
  }
  throw Error("service not ready: "+url);
}

async function main(){
  const binary=path.resolve(process.argv[2]), python=process.argv[3]||"python";
  const scenario=process.argv[4] ? JSON.parse(await fs.readFile(process.argv[4],"utf8")) : {
    name:"同学甲", completed:"C102", first:"C302", second:"C490", blocked:"C301",
    preference1:"嵌入式",preference2:"工程沟通", goal:"信号 嵌入式",credits:4,
  };
  const dir=await fs.mkdtemp(path.join(os.tmpdir(),"course-advisor-e2e-"));
  const screenshots=process.env.ADVISOR_SCREENSHOTS;
  const phase4=await port(), p=await port();
  const graph="http://127.0.0.1:"+phase4, url="http://127.0.0.1:"+p;
  const root=path.resolve(__dirname,".."), phase4Root=path.resolve(root,"../prerequisite-analysis");
  const args=["--db",path.join(dir,"course-advisor.sqlite3"),"--port",String(p),"--prereq",graph];
  if(process.env.ADVISOR_EVIDENCE)args.push("--evidence",process.env.ADVISOR_EVIDENCE);else args.push("--sample-evidence");
  if(process.env.ADVISOR_RAG)args.push("--rag",process.env.ADVISOR_RAG);
  if(process.env.ADVISOR_OLLAMA)args.push("--ollama",process.env.ADVISOR_OLLAMA,"--model",process.env.ADVISOR_MODEL);
  let upstream, api, browser;
  try{
    upstream=processOf(python,["-m","prereq.app","--port",String(phase4)],phase4Root);
    await checkReady(graph,upstream.child);
    api=processOf(binary,args,root);
    const readiness=await checkReady(url,api.child);
    assert(readiness.courses>0);
    if(screenshots)await fs.mkdir(screenshots,{recursive:true});
    browser=await chromium.launch();
    const page=await browser.newPage({viewport:{width:1440,height:1000}});
    const errors=[];
    page.on("pageerror",error=>errors.push(error.message));
    const visible=locator=>locator.waitFor({state:"visible",timeout:25000});
    const button=name=>page.getByRole("button",{name,exact:true});
    const input=name=>page.getByLabel(name,{exact:true});
    const shoot=async file=>{if(screenshots)await page.screenshot({path:path.join(screenshots,file+".png"),fullPage:true});};
    await page.goto(url);
    await visible(page.locator("#source").filter({hasText:"课程快照"}));
    await input("称呼").fill(scenario.name);
    await input("培养阶段").fill("大三");
    await input("选课偏好").fill(scenario.preference1);
    await page.locator("#course-search").fill(scenario.completed);
    await page.locator("#completed input[value='"+scenario.completed+"']").check();
    await button("保存画像").click();
    await visible(page.locator("#revision").filter({hasText:"版本 1"}));
    await shoot("01-学生画像");
    await input("目标").fill(scenario.goal);
    await input("学分上限").fill(String(scenario.credits));
    await input("推荐门数").fill("1");
    await input("候选课程编号").fill([scenario.first,scenario.second,scenario.blocked].join(","));
    await shoot("02-选课目标");
    await button("生成方案").click();
    await visible(page.getByText("结果已保存",{exact:true}));
    await visible(page.getByRole("heading",{name:"建议课程"}));
    assert.match(await page.locator("#result table").first().innerText(),new RegExp(scenario.first));
    assert.match(await page.locator("#result").innerText(),/缺少必需先修/);
    assert.match(await page.locator("#result").innerText(),/方案版本 1/);
    assert((await page.locator(".reference").count())>0,"evidence must be rendered");
    assert(!/mock|fixture/i.test(await page.locator("header").innerText()));
    await shoot("03-任务进度与推荐");
    await page.getByRole("heading",{name:"培养方案依据"}).scrollIntoViewIfNeeded();
    if(screenshots)await page.screenshot({path:path.join(screenshots,"04-约束与培养方案依据.png")});
    await button("历史版本").click();
    await visible(button("查看并调整"));
    await shoot("05-历史记录");
    await button("查看并调整").click();
    await input("选课偏好").fill(scenario.preference2);
    await button("保存画像").click();
    await visible(page.locator("#revision").filter({hasText:"版本 2"}));
    await button("生成方案").click();
    await visible(page.locator(".measure").filter({hasText:"方案版本 2"}));
    assert.match(await page.locator("#result table").first().innerText(),new RegExp(scenario.second));
    await shoot("06-重新规划");
    const check=await page.evaluate(async()=>{
      const token=localStorage.getItem("course.advisor.token");
      const id=localStorage.getItem("course.advisor.run");
      const response=await fetch("/api/v1/runs/"+id,{headers:{Authorization:"Bearer "+token}});
      return response.json();
    });
    assert.equal(check.plan.snapshot_version,readiness.snapshot_version);
    if(process.env.ADVISOR_OLLAMA)console.log("Local model result:",check.plan.explanation_mode,"tokens:",check.plan.tokens??"unavailable");
    await api.stop();api=processOf(binary,args,root);await checkReady(url,api.child);
    await page.reload();await visible(page.locator(".measure").filter({hasText:"方案版本 2"}));
    await button("历史版本").click();
    await visible(page.locator("#history-list button").nth(1));
    assert.equal(await page.locator("#history-list button").count(),2);
    assert.deepEqual(errors,[]);
    await page.setViewportSize({width:390,height:844});
    assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));
    console.log("PASS: actual SQLite and Phase 4 HTTP, two plan versions, persisted restart, SSE, evidence and mobile");
    console.log(JSON.stringify({courses:readiness.courses,profile_revision:check.plan.profile_revision,
      snapshot:check.plan.snapshot_version,version:check.version,mode:check.plan.evidence_mode,
      model:check.plan.explanation_mode,tokens:check.plan.tokens??null,graph_calls:check.plan.tool_calls}));
  }finally{
    if(browser)await browser.close();
    if(api)await api.stop();
    if(upstream)await upstream.stop();
    const tmp=await fs.realpath(os.tmpdir()), resolved=await fs.realpath(dir);
    if(path.dirname(resolved)===tmp&&path.basename(resolved).startsWith("course-advisor-e2e-")){
      await fs.rm(resolved,{recursive:true,force:true});
    }
  }
}
main().catch(e=>{console.error(e);process.exitCode=1;});
