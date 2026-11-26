const assert=require("node:assert/strict");
const {chromium}=require("playwright");

async function main(){
  const base=process.argv[2]||"http://127.0.0.1:18095";
  const browser=await chromium.launch();
  try{
    const p=await browser.newPage({viewport:{width:1440,height:900}});
    const errors=[];
    p.on("pageerror",e=>errors.push(e.message));
    await p.goto(base);
    await p.locator("#source").filter({hasText:"课程快照"}).waitFor();
    await p.getByLabel("称呼").fill("联调同学");
    await p.locator("#course-search").fill("C102");
    await p.locator("#completed input").check();
    await p.getByRole("button",{name:"保存画像",exact:true}).click();
    await p.locator("#revision").filter({hasText:"版本 1"}).waitFor();
    await p.getByLabel("目标",{exact:true}).fill("嵌入式");
    await p.getByLabel("候选课程编号").fill("C302");
    await p.getByRole("button",{name:"生成方案",exact:true}).click();
    await p.getByText("结果已保存",{exact:true}).waitFor();
    assert.match(await p.locator("#result").innerText(),/嵌入式应用/);
    const response=await p.evaluate(async()=>{
      const id=localStorage.getItem("course.advisor.run");
      const token=localStorage.getItem("course.advisor.token");
      const res=await fetch("/api/v1/runs/"+id+"/events",{headers:{Authorization:"Bearer "+token,"Last-Event-ID":"4"}});
      return {status:res.status,content:res.headers.get("content-type"),body:await res.text()};
    });
    assert.equal(response.status,200);
    assert.match(response.content,/text\/event-stream/);
    assert.match(response.body,/id: 5\nevent: plan_generated/);
    assert.match(response.body,/id: 6\nevent: completed/);
    assert(!response.body.includes("id: 4\n"));
    const metrics=await (await fetch(base+"/metrics")).text();
    assert.match(metrics,/advisor_sql_pool_open/);
    assert.match(metrics,/advisor_http_duration_seconds_bucket/);
    assert.deepEqual(errors,[]);
    console.log("PASS: Nginx browser, two-instance workspace, replay and real metrics");
  }finally{await browser.close();}
}
main().catch(e=>{console.error(e);process.exitCode=1;});
