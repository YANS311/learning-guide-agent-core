// Starts two actual advisor processes. All connections are to the synthetic lab.
const assert=require("node:assert/strict");
const {spawn}=require("node:child_process");
const {once}=require("node:events");
const net=require("node:net");
const http=require("node:http");
const path=require("node:path");

async function openPort(){
  const socket=net.createServer();socket.listen(0,"127.0.0.1");
  await once(socket,"listening");
  const p=socket.address().port;
  await new Promise(resolve=>socket.close(resolve));return p;
}
function child(exe,args,cwd){
  const proc=spawn(exe,args,{cwd,windowsHide:true,stdio:["ignore","ignore","inherit"]});
  const exit=once(proc,"exit");
  return {proc,stop:async(signal="SIGTERM")=>{if(proc.exitCode===null&&proc.signalCode===null)proc.kill(signal);await exit;}};
}
const wait=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function ready(base){
  for(let n=0;n<55;n++){
    try{if((await fetch(base+"/health/ready",{signal:AbortSignal.timeout(800)})).ok)return;}catch{}
    await wait(250);
  }
  throw Error("lab process not ready: "+base);
}
function compareStatus(status,expected){assert.equal(status,expected);}
async function call(origin,url,token,method="GET",body,key){
  const headers={};
  if(token)headers.Authorization="Bearer "+token;
  if(body!==undefined)headers["Content-Type"]="application/json";
  if(key)headers["Idempotency-Key"]=key;
  const response=await fetch(origin+url,{method,headers,body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(5500)});
  return {status:response.statusCode??response.status,data:await response.json()};
}
async function eventStream(base,id,token,last,stopKind){
  const controller=new AbortController();
  const timeout=setTimeout(()=>controller.abort(),18000);
  let reader;
  try{
    const res=await fetch(base+"/api/v1/runs/"+id+"/events",
      {headers:{Authorization:"Bearer "+token,"Last-Event-ID":String(last)},signal:controller.signal});
    assert.equal(res.status,200);
    reader=res.body.getReader();
    const decoder=new TextDecoder();
    let partial="";
    const events=[];
    while(true){
      const item=await reader.read();
      if(item.done)return events;
      partial+=decoder.decode(item.value,{stream:true});
      let at;
      while((at=partial.indexOf("\n\n"))>=0){
        const frame=partial.slice(0,at);partial=partial.slice(at+2);
        const seq=Number(frame.match(/^id: (\d+)$/m)?.[1]);
        const kind=frame.match(/^event: (.+)$/m)?.[1];
        if(!kind)continue;
        assert(seq>last);
        last=seq;events.push({seq,kind});
        if(kind===stopKind)return events;
      }
    }
  }finally{controller.abort();clearTimeout(timeout);if(reader)await reader.cancel().catch(()=>{});}
}
async function main(){
  assert(process.env.ADVISOR_MYSQL_DSN&&process.env.ADVISOR_LAB_MARKER,"requires isolated MySQL");
  const exe=path.resolve(process.argv[2]),python=process.argv[3]||"python";
  const root=path.resolve(__dirname,"..");
  const p=await openPort(),pa=await openPort(),pb=await openPort();
  const graph="http://127.0.0.1:"+p,a="http://127.0.0.1:"+pa,b="http://127.0.0.1:"+pb;
  const args=(port,up)=>["--port",String(port),"--mysql-lab","--prereq",up,"--sample-evidence"];
  const openRun="/api/v1/runs";
  let phase,first,second,slow;
  try{
    phase=child(python,["-m","prereq.app","--port",String(p)],path.resolve(root,"../prerequisite-analysis"));
    await ready(graph);
    slow=http.createServer(async(req,res)=>{
      if(!req.url.startsWith("/api/v1/courses")&&!req.url.startsWith("/api/v1/prerequisites/")){res.writeHead(404).end();return;}
      if(req.url.startsWith("/api/v1/prerequisites/"))await wait(1400);
      try{
        const response=await fetch(graph+req.url,{signal:AbortSignal.timeout(3000)});
        if(!res.destroyed)res.writeHead(response.status,{"Content-Type":"application/json"}).end(await response.text());
      }catch{if(!res.destroyed)res.writeHead(503).end();}
    });
    slow.listen(0,"127.0.0.1");await once(slow,"listening");
    const delayed="http://127.0.0.1:"+slow.address().port;
    first=child(exe,args(pa,delayed),root);
    await ready(a);
    const initial=await call(a,"/api/v1/workspaces",null,"POST",{});
    compareStatus(initial.status,201);
    const token=initial.data.token;
    const name="Synthetic worker recovery";
    compareStatus((await call(a,"/api/v1/profile",token,"PUT",{revision:0,name,stage:"",direction:"",preference:"",completed:["C102"]})).status,200);
    const req={profile_revision:1,goal:"嵌入式",course_ids:["C302"],max_credits:4,limit:1};
    const key=crypto.randomUUID();
    const task=await call(a,openRun,token,"POST",req,key);
    compareStatus(task.status,202);
    const id=task.data.id;
    const initialEvents=await eventStream(a,id,token,0,"retrieval_completed");
    assert(initialEvents.some(x=>x.kind==="run_started"));
    await first.stop("SIGKILL");first=null;
    second=child(exe,args(pb,graph),root);await ready(b);
    const resumed=await eventStream(b,id,token,initialEvents.at(-1).seq,"completed");
    assert(resumed.some(x=>x.kind==="requeued")&&resumed.at(-1).kind==="completed");
    const plan=await call(b,openRun+"/"+id,token);
    assert.equal(plan.data.state,"SUCCEEDED");
    assert.deepEqual(plan.data.plan.selected.map(c=>c.id),["C302"]);
    const replay=await call(b,openRun,token,"POST",req,key);
    assert.equal(replay.status,200);assert.equal(replay.data.id,id);
    const repeat=await eventStream(b,id,token,0,"completed");
    assert.equal(repeat.filter(e=>e.kind==="completed").length,1);
    assert.equal(repeat.filter(e=>e.kind==="run_started").length,2);
    const outsider=(await call(b,"/api/v1/workspaces",null,"POST",{})).data.token;
    assert.equal((await call(b,openRun+"/"+id+"/events",outsider)).status,404);
    first=child(exe,args(pa,graph),root);await ready(a);
    const key2=crypto.randomUUID();
    const submissions=await Promise.all([call(a,openRun,token,"POST",req,key2),call(b,openRun,token,"POST",req,key2)]);
    assert.deepEqual(submissions.map(r=>r.status).sort(),[200,202]);
    assert.equal(submissions[0].data.id,submissions[1].data.id);
    const secondID=submissions[0].data.id;
    const race=await call(b,openRun+"/"+secondID+"/cancel",token,"POST");
    assert([200,409].includes(race.status));
    const end=await eventStream(a,secondID,token,0,"cancelled");
    assert.equal(end.filter(x=>["cancelled","completed"].includes(x.kind)).length,1);
    const history=await call(b,openRun,token);
    assert.equal(history.data.length,2);
    console.log(JSON.stringify({forced_worker_exit:"PASS",recovery_attempts:2,replay_events:repeat.map(x=>x.kind),
      shared_idempotency:"PASS",workspace_isolation:"PASS",cancellation_terminal:end.at(-1).kind,
      database_result:plan.data.state,selected:plan.data.plan.selected.map(c=>c.id)}));
  }finally{
    if(first)await first.stop();
    if(second)await second.stop();
    if(phase)await phase.stop();
    if(slow){slow.closeAllConnections();await new Promise(resolve=>slow.close(resolve));}
  }
}
main().catch(err=>{console.error(err);process.exitCode=1;});
