"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const net = require("node:net");
const path = require("node:path");
const { once } = require("node:events");
const { spawn } = require("node:child_process");
const { chromium } = require("playwright");
const binary = process.argv[2];
const captures = process.env.GRAPHOPS_SCREENSHOTS;
const admin = process.env.GRAPHOPS_ADMIN_TOKEN;

async function unusedPort() {
  const server = net.createServer();
  server.listen(0,"127.0.0.1");
  await once(server,"listening");
  const port = server.address().port;
  await new Promise(resolve=>server.close(resolve));
  return port;
}

async function start(children, overrides = {}) {
  const port = await unusedPort();
  const child = spawn(binary,["-listen","127.0.0.1:"+port],
    {env:{...process.env,...overrides},stdio:"ignore",windowsHide:true});
  children.push(child);
  const base = "http://127.0.0.1:"+port;
  for(let i=0;i<80;i++){
    if(child.exitCode!==null) throw Error("Graph Ops exited before becoming live");
    try { if((await fetch(base+"/health/live")).ok) return base; } catch {}
    await new Promise(r=>setTimeout(r,100));
  }
  throw Error("Graph Ops did not start");
}
async function capture(page, name) {
  if (!captures) return;
  await fs.mkdir(captures,{recursive:true});
  await page.screenshot({path:path.join(captures,name+".png"),fullPage:true,animations:"disabled"});
}
async function text(page, value) {
  await page.getByText(value,{exact:true}).first().waitFor({state:"visible"});
}
async function signIn(page) {
  await page.getByLabel("Administrator token").fill(admin);
  await page.getByRole("button",{name:"Sign in",exact:true}).click();
  await text(page,"Operations overview");
}
async function tab(page,name) {
  await page.getByRole("navigation").getByRole("button",{name,exact:true}).click();
}
function payload(id) {
  return {name:"synthetic.json",mimeType:"application/json",buffer:Buffer.from(JSON.stringify({
    courses:[{id,name:"Synthetic "+id}],relations:[]
  }))};
}
async function load(page,file) {
  await tab(page,"Data import");
  await page.locator("#file").setInputFiles(file);
  await page.getByRole("button",{name:"Validate file"}).click();
}
async function preview(page) {
  await page.getByRole("button",{name:"Open sync preview"}).click();
  await page.getByRole("button",{name:"Build preview"}).click();
  await text(page,"Preview ready");
}
async function confirm(page) {
  await page.getByLabel("Confirm these incremental changes").check();
  await page.getByRole("button",{name:"Execute sync"}).click();
}
async function requestAdmin(base) {
  const login = await fetch(base+"/api/v1/admin/login",{method:"POST",headers:{"Content-Type":"application/json"},
    body:JSON.stringify({token:admin})});
  assert.equal(login.status,200);
  const {session}=await login.json();
  return async (name,data,key)=>{
    const h={"Authorization":"Bearer "+session,"Content-Type":"application/json"};
    if(key)h["Idempotency-Key"]=key;
    const out=await fetch(base+name,{method:data?"POST":"GET",headers:h,body:data?JSON.stringify(data):undefined});
    assert.equal(out.status,200);
    return out.json();
  };
}
async function main(){
  const browser=await chromium.launch({headless:true});
  const children=[];
  try {
    const base=await start(children);
    const page=await browser.newPage({viewport:{width:1360,height:850}});
    const faults=[];
    page.on("pageerror",error=>faults.push(error.message));
    assert.equal((await fetch(base+"/api/v1/graph/db/execute",
      {method:"POST",headers:{"Content-Type":"application/json"},body:"{}"})).status,401);
    await page.goto(base+"/console");
    await capture(page,"01-login");
    await page.getByLabel("Administrator token").fill("not-a-valid-token");
    await page.getByRole("button",{name:"Sign in",exact:true}).click();
    await page.locator("#login-error").waitFor({state:"visible"});
    await text(page,"login_rejected · auth · administrator session could not be created");
    await signIn(page);
    await text(page,"Ready");
    await capture(page,"02-overview");
    await load(page,{name:"bad.csv",mimeType:"text/csv",buffer:Buffer.from("bad,header\n")});
    await text(page,"Import rejected");
    await load(page,path.join(__dirname,"../testdata/console.csv"));
    await text(page,"Validation passed");
    await capture(page,"03-import");
    await page.getByRole("button",{name:"Open sync preview"}).click();
    await page.getByLabel("Managed course IDs").fill("LAB-E2E-A");
    await page.getByRole("button",{name:"Build preview"}).click();
    await text(page,"Preview rejected");
    await page.getByLabel("Managed course IDs").fill("LAB-E2E-A, LAB-E2E-B");
    await page.getByRole("button",{name:"Build preview"}).click();
    await text(page,"Preview ready");
    await capture(page,"04-preview");
    let executions=0;
    page.on("request",r=>{if(r.url().endsWith("/db/execute")) executions++});
    await page.getByLabel("Confirm these incremental changes").check();
    await page.locator("#execute").evaluate(b=>{b.click();b.click()});
    await text(page,"Sync committed");
    assert.equal(executions,1);
    await capture(page,"05-committed");
    await tab(page,"Operation history");
    await page.getByRole("button",{name:"View details"}).first().click();
    await text(page,"Committed operation");
    await capture(page,"06-history");
    await tab(page,"Monitoring");
    await text(page,"Average transaction latency");
    await capture(page,"07-monitoring");
    await load(page,payload("LAB-E2E-LOST"));
    await text(page,"Validation passed");
    await preview(page);
    let key;
    await page.route("**/api/v1/graph/db/execute",async route=>{
      key=route.request().headers()["idempotency-key"];
      const committed=await route.fetch();
      assert.equal(committed.status(),200);
      await route.abort("failed");
    },{times:1});
    await confirm(page);
    await text(page,"Outcome not confirmed");
    assert.equal(await page.inputValue("#operation-key"),key);
    await capture(page,"08-response-lost");
    await page.reload();
    await signIn(page);
    await tab(page,"Sync preview");
    assert.equal(await page.inputValue("#operation-key"),key);
    await page.getByRole("button",{name:"Check outcome"}).click();
    await text(page,"Committed record found");
    await load(page,payload("LAB-E2E-STALE"));
    await text(page,"Validation passed");
    await preview(page);
    const call=await requestAdmin(base);
    const target={courses:[{id:"LAB-E2E-DRIFT",name:"Synthetic drift"}],relations:[]};
    const p=await call("/api/v1/graph/db/preview",{scope:{course_ids:["LAB-E2E-DRIFT"]},target});
    await call("/api/v1/graph/db/execute",{plan_token:p.plan_token,target},"synthetic-browser-drift");
    await confirm(page);
    await text(page,"Sync rejected");
    await page.getByText(/database_state_changed/).waitFor({state:"visible"});
    const shortPlan=await start(children,{GRAPHOPS_LAB_PLAN_SECONDS:"1"});
    const expiry=await browser.newPage();
    await expiry.goto(shortPlan+"/console");
    await signIn(expiry);
    await load(expiry,payload("LAB-E2E-EXPIRED"));
    await text(expiry,"Validation passed");
    await preview(expiry);
    await expiry.waitForTimeout(1200);
    await confirm(expiry);
    await text(expiry,"Sync rejected");
    await expiry.getByText(/plan_expired/).waitFor({state:"visible"});
    const shortSession=await start(children,{GRAPHOPS_ADMIN_SESSION_SECONDS:"1"});
    const auth=await browser.newPage();
    await auth.goto(shortSession+"/console");
    await signIn(auth);
    await text(auth,"Ready");
    await auth.waitForTimeout(1200);
    await auth.getByRole("button",{name:"Refresh",exact:true}).filter({visible:true}).click();
    await text(auth,"Session expired. Sign in to continue.");
    const unavailable=await start(children,{GRAPHOPS_LAB_BOLT:"bolt://127.0.0.1:"+await unusedPort()});
    const outage=await browser.newPage();
    await outage.goto(unavailable+"/console");
    await signIn(outage);
    await text(outage,"Unavailable");
    const mobile=await browser.newPage({viewport:{width:390,height:844}});
    await mobile.goto(base+"/console");
    await signIn(mobile);
    await text(mobile,"Ready");
    assert.equal(await mobile.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),true);
    assert.deepEqual(faults,[]);
    console.log("PASS: browser workflow, authorization, repeat click, HTTP reply loss, refresh recovery, stale and expired plans, dependency outage, mobile layout");
  } finally {
    await browser.close();
    for(const child of children) {
      if(child.exitCode===null && child.signalCode===null) {
        const finished=once(child,"exit");child.kill();await finished;
      }
    }
  }
}
main().catch(error=>{console.error(error);process.exitCode=1});
