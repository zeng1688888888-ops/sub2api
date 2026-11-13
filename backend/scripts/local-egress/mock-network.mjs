import http from 'node:http';
import { createHash, randomUUID } from 'node:crypto';
let holdMs=2500;
const lanes=Object.fromEntries([18101,18102,18103].map(port=>[port,{active:0,peak:0,total:0}]));
let totalActive=0,totalPeak=0,identities=new Set();
const snapshot=()=>({kind:'local simulated upstream and proxies',holdMs,totalActive,totalPeak,credentialIdentities:identities.size,lanes});
const json=(res,code,data)=>{res.writeHead(code,{'Content-Type':'application/json'});res.end(JSON.stringify(data));};
const upstream=http.createServer(async(req,res)=>{
 if(req.url==='/admin/metrics')return json(res,200,snapshot());
 if(req.url.startsWith('/admin/reset')){if(totalActive)return json(res,409,{error:'requests still active'});const u=new URL(req.url,'http://localhost');holdMs=Number(u.searchParams.get('hold_ms')||2500);for(const l of Object.values(lanes)){l.peak=0;l.total=0;}totalPeak=0;identities.clear();return json(res,200,snapshot());}
 if(req.url.endsWith('/models'))return json(res,200,{object:'list',data:[{id:'gpt-4o',object:'model',owned_by:'local-mock'}]});
 if(!req.url.includes('/responses')&&!req.url.includes('/chat/completions'))return json(res,404,{error:'local mock endpoint not implemented'});
 let raw='';for await(const chunk of req)raw+=chunk;
 let body;try{body=JSON.parse(raw);}catch{return json(res,400,{error:'invalid JSON'});}
 identities.add(createHash('sha256').update(req.headers.authorization||'').digest('hex'));
 const id='resp_local_'+randomUUID().replaceAll('-','');const model=body.model||'gpt-4o';
 const completed={id,object:'response',created_at:Math.floor(Date.now()/1000),status:'completed',model,output:[{id:'msg_local',type:'message',role:'assistant',status:'completed',content:[{type:'output_text',text:'Local egress test completed.',annotations:[]}]}],usage:{input_tokens:10,output_tokens:6,total_tokens:16,input_tokens_details:{cached_tokens:0}}};
 if(body.stream){res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-cache','Connection':'keep-alive'});res.flushHeaders();res.write('data: '+JSON.stringify({type:'response.created',response:{...completed,status:'in_progress',output:[],usage:null}})+'\n\n');}
 const timer=setTimeout(()=>{if(res.destroyed)return;if(body.stream){res.write('data: '+JSON.stringify({type:'response.output_text.delta',output_index:0,content_index:0,item_id:'msg_local',delta:'Local egress test completed.'})+'\n\n');res.end('data: '+JSON.stringify({type:'response.completed',response:completed})+'\n\n');}else json(res,200,completed);},holdMs);
 res.on('close',()=>clearTimeout(timer));
});
upstream.listen(18100,'127.0.0.1');
const servers=[upstream];
for(const port of [18101,18102,18103]){
 const proxy=http.createServer((req,res)=>{
  let target;try{target=new URL(req.url);}catch{return json(res,400,{error:'absolute proxy URL required'});}
  if(target.hostname!=='127.0.0.1'||target.port!=='18100'||target.protocol!=='http:')return json(res,403,{error:'this test proxy only allows the local mock upstream'});
  const tracked=target.pathname.includes('/responses')||target.pathname.includes('/chat/completions');const lane=lanes[port];let released=false;
  if(tracked){lane.active++;lane.peak=Math.max(lane.peak,lane.active);lane.total++;totalActive++;totalPeak=Math.max(totalPeak,totalActive);}
  const release=()=>{if(tracked&&!released){released=true;lane.active--;totalActive--;}};
  const headers={...req.headers,host:target.host,'x-local-egress':String(port)};delete headers['proxy-authorization'];delete headers['proxy-connection'];
  const outgoing=http.request(target,{method:req.method,headers},up=>{res.writeHead(up.statusCode,up.headers);up.pipe(res);up.on('end',release);up.on('error',()=>{release();res.destroy();});});
  outgoing.on('error',()=>{release();if(!res.headersSent)json(res,502,{error:'local proxy forwarding failed'});else res.destroy();});
  res.on('close',()=>{release();outgoing.destroy();});req.pipe(outgoing);
 });
 proxy.listen(port,'127.0.0.1');servers.push(proxy);
}
console.log('Mock upstream 18100 and three isolated local proxies 18101/18102/18103 ready.');
for(const signal of ['SIGINT','SIGTERM'])process.on(signal,()=>{for(const s of servers)s.close();});
