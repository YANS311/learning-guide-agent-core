const assert = require("node:assert/strict");
const {spawn} = require("node:child_process");
const {once} = require("node:events");
const {mkdir, readFile} = require("node:fs/promises");
const net = require("node:net");
const path = require("node:path");
const {chromium} = require("playwright");

async function main() {
  const fixture=process.argv[3] ? JSON.parse(await readFile(process.argv[3],"utf8")) : {
    target:"C401", nodes:7, edges:9, search:"C301", searchName:"数字信号处理",
    compare:"C302", completedName:"数字信号处理", completed:"C301", missing:"C302",
    impact:"C101", isolated:"C490", isolatedName:"工程沟通",
  };
  const server = net.createServer();
  server.listen(0,"127.0.0.1");
  await once(server,"listening");
  const {port} = server.address();
  await new Promise(resolve=>server.close(resolve));
  const url=`http://127.0.0.1:${port}`;
  const child=spawn(process.argv[2] || "python",["-m","prereq.app","--port",String(port)],
    {cwd:path.join(__dirname,".."),stdio:["ignore","ignore","inherit"],windowsHide:true});
  const ended=once(child,"exit");
  const dir=process.env.PREREQ_SCREENSHOTS;
  let browser;
  try {
    let ready=false;
    for(let i=0;i<60;i++){
      if(child.exitCode!==null) break;
      try { if((await fetch(url+"/health/ready")).ok){ready=true;break;} }catch {}
      await new Promise(resolve=>setTimeout(resolve,250));
    }
    assert(ready,"prerequisite service readiness");
    if(dir) await mkdir(dir,{recursive:true});
    browser=await chromium.launch();
    const page=await browser.newPage({viewport:{width:1440,height:1000}});
    const errors=[];
    page.on("pageerror",e=>errors.push(e.message));
    const shoot=async(name)=>{
      if(dir) await page.screenshot({path:path.join(dir,name+".png"),fullPage:true});
    };
    const visible=async locator=>locator.waitFor({state:"visible"});
    const tab=async key=>page.locator(`[data-mode="${key}"]`).click();
    const select=async id=>page.locator(`#catalog [data-id="${id}"]`).click();
    const heading=title=>page.getByRole("heading",{name:title,exact:true});
    await page.goto(url);
    await select(fixture.target);
    await visible(page.getByRole("group",{name:"先修关系有向图"}));
    assert.equal(await page.locator(".graph-node").count(),fixture.nodes);
    assert.equal(await page.locator("path[marker-end]").count(),fixture.edges);
    assert(!/mock|fixture/i.test(await page.locator("header").innerText()));
    await shoot("01-先修图谱");
    await page.locator("#keyword").fill(fixture.search);
    await page.getByRole("button",{name:"检索",exact:true}).click();
    await visible(page.locator("#search-count").filter({hasText:/^找到 1 门课程$/}));
    assert.equal(await page.locator("#catalog button").count(),1);
    await select(fixture.search);
    await visible(page.getByRole("button",{name:"查看 "+fixture.searchName,exact:true}));
    await shoot("02-课程检索");
    await page.locator("#keyword").fill("");
    await page.getByRole("button",{name:"检索",exact:true}).click();
    await select(fixture.target);
    await tab("order");
    await visible(heading("可行学习顺序"));
    assert((await page.locator("tbody tr").last().innerText()).includes(fixture.target));
    await shoot("03-学习顺序");
    await tab("compare");
    await page.locator("#other").selectOption(fixture.compare);
    await page.getByRole("button",{name:"开始比较"}).click();
    await visible(heading("共同先修到两目标的路径"));
    await shoot("04-依赖比较");
    await select(fixture.missingTarget || fixture.target);
    await tab("missing");
    await page.getByLabel(fixture.completedName,{exact:true}).check();
    await page.getByRole("button",{name:"分析缺失项"}).click();
    await visible(page.getByRole("row").filter({hasText:"已满足直接先修"}).filter({hasText:fixture.completed}));
    await visible(page.getByRole("row").filter({hasText:"未满足直接先修"}).filter({hasText:fixture.missing}));
    await shoot("05-缺失先修");
    await select(fixture.impact);
    await tab("impact");
    await visible(heading("后续依赖课程"));
    await visible(page.getByRole("row").filter({hasText:"间接"}).filter({hasText:fixture.target}));
    await shoot("06-依赖影响");
    await select(fixture.isolated); await tab("graph");
    await visible(page.getByRole("button",{name:"查看 "+fixture.isolatedName,exact:true}));
    assert.equal(await page.locator(".graph-node").count(),1);
    await page.setViewportSize({width:390,height:844});
    await select(fixture.target);
    await visible(heading("依赖层级"));
    assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));
    const scroll=await page.locator(".graph-scroll").evaluate(box=>box.scrollWidth>box.clientWidth);
    assert(scroll,"mobile graph scroll stays inside panel");
    await shoot("07-移动端");
    assert.deepEqual(errors,[]);
    console.log("PASS: six Chinese workflows, empty upstream, mobile layout and no page errors");
  } finally {
    if(browser) await browser.close();
    if(child.exitCode===null) child.kill();
    await ended;
  }
}
main().catch(error=>{console.error(error); process.exitCode=1;});
