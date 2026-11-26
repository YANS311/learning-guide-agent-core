// Use only with fresh synthetic lab processes and their isolated database.
const assert=require("node:assert/strict");
const fs=require("node:fs/promises");
const {performance}=require("node:perf_hooks");
const wait=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const draft={profile_revision:1,goal:"嵌入式",course_ids:["C302"],limit:1,max_credits:4};

async function send(origin,path,token,method="GET",body,key){
  const headers={};
  if(token)headers.Authorization="Bearer "+token;
  if(body!==undefined)headers["Content-Type"]="application/json";
  if(key)headers["Idempotency-Key"]=key;
  const start=performance.now();
  try{
    const r=await fetch(origin+path,{method,headers,body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(6500)});
    const data=await r.json();
    return {status:r.status,data,ms:performance.now()-start};
  }catch{return {status:0,data:null,ms:performance.now()-start};}
}
async function batch(n,c,fn){
  const start=performance.now(), out=[];
  let next=0;
  await Promise.all(Array.from({length:Math.min(n,c)},async()=>{
    while(next<n){const i=next++;out[i]=await fn(i);}
  }));
  return {out,seconds:(performance.now()-start)/1000};
}
function stats(out,seconds){
  const sorted=out.map(x=>x.ms).sort((a,b)=>a-b);
  const counts={};
  for(const x of out)counts[x.status]=(counts[x.status]||0)+1;
  const pick=p=>sorted[Math.ceil(p*sorted.length)-1]??null;
  return {n:out.length,seconds,rps:out.length/seconds,p50:pick(.5),p95:pick(.95),p99:pick(.99),counts};
}
async function account(base){
  const r=await send(base,"/api/v1/workspaces",null,"POST",{});
  assert.equal(r.status,201);
  const token=r.data.token;
  const save=await send(base,"/api/v1/profile",token,"PUT",
    {name:"Synthetic load workspace",revision:0,completed:["C102"],direction:"",stage:"",preference:""});
  assert.equal(save.status,200);
  return token;
}
async function finish(base,token,id,elapsed){
  const start=performance.now();
  while(performance.now()-start<27000){
    const r=await send(base,"/api/v1/runs/"+id,token);
    if(r.status!==200)return {status:r.status,ms:elapsed+performance.now()-start};
    const run=r.data;
    if(run.state==="SUCCEEDED"){
      assert.deepEqual(run.plan.selected.map(c=>c.id),["C302"]);
      return {status:200,ms:elapsed+performance.now()-start};
    }
    if(!["RUNNING","QUEUED"].includes(run.state))return {status:500,ms:elapsed+performance.now()-start};
    await wait(70);
  }
  return {status:0,ms:elapsed+performance.now()-start};
}
async function main(){
  const targets=JSON.parse(process.env.ADVISOR_LOAD_TARGETS);
  let results={settings:{rounds:3,http_concurrency:[1,8,32,64,128],task_concurrency:[1,8,32],rule_mode:true},targets:{}};
  try{results=JSON.parse(await fs.readFile(process.argv[2],"utf8"));}catch(e){if(e.code!=="ENOENT")throw e;}
  for(const [name,origins] of Object.entries(targets)){
    for(const base of origins){
      const u=new URL(base);
      assert(u.protocol==="http:"&&u.hostname==="127.0.0.1"&&u.port,"loopback test targets only");
      const ready=await send(base,"/health/ready");
      assert.equal(ready.data?.snapshot_version,"29cee747e9d778dd784c1f26aece1b44551a1df72c94e4a4ff9c3194f579aa42","synthetic snapshot only");
    }
    const only=origins[0], origin=i=>origins[i%origins.length];
    const workspace=await account(only);
    const base=await send(only,"/api/v1/runs",workspace,"POST",draft,crypto.randomUUID());
    assert.equal(base.status,202);
    assert.equal((await finish(only,workspace,base.data.id,base.ms)).status,200);
    const run={read:[],task:[],metrics:[]};
    for(const endpoint of ["/api/v1/courses","/api/v1/runs?limit=10"]){
      await batch(10,4,i=>send(origin(i),endpoint,workspace));
      for(const c of [1,8,32,64,128])for(let round=1;round<=3;round++){
        const r=await batch(Math.max(64,c*2),c,i=>send(origin(i),endpoint,workspace));
        run.read.push({endpoint,concurrency:c,round,...stats(r.out,r.seconds)});
      }
    }
    const pool=(await batch(32,4,()=>account(only))).out;
    for(const c of [1,8,32])for(let round=1;round<=3;round++){
      const started=performance.now();
      const r=await batch(c,c,i=>send(origin(i),"/api/v1/runs",pool[i],"POST",draft,crypto.randomUUID()));
      const done=await batch(c,c,i=>r.out[i].status===202?finish(origin(i),pool[i],r.out[i].data.id,r.out[i].ms):Promise.resolve(r.out[i]));
      run.task.push({concurrency:c,round,enqueue:stats(r.out,r.seconds),
        endToEnd:stats(done.out,(performance.now()-started)/1000)});
    }
    for(const url of origins){
      try{
        const lines=(await (await fetch(url+"/metrics")).text()).split("\n");
        run.metrics.push(lines.filter(line=>/^advisor_(queue_depth|go_goroutines|heap_alloc_bytes|sql_pool_open|sql_pool_wait_total|cache_hits_total|cache_misses_total|workers_active|sse_open) /.test(line)));
      }catch{run.metrics.push([]);}
    }
    results.targets[name]=run;
    console.log("completed",name,run.read.length,"read rounds",run.task.length,"task rounds");
    await fs.writeFile(process.argv[2],JSON.stringify(results,null,2)+"\n");
  }
}
main().catch(e=>{console.error(e);process.exitCode=1;});
