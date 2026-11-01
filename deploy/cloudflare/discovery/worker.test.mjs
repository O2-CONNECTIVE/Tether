import test from "node:test";
import assert from "node:assert/strict";
import {discovery,GATE_VALUE} from "./worker.mjs";
import {createNativeWorker} from "../native/worker.mjs";
const now=1793490000000;
const env=()=>({TETHER_DISCOVERY_GATE:GATE_VALUE,TETHER_DISCOVERY_STARTS_AT:String(now),TETHER_DISCOVERY_EXPIRES_AT:String(now+300000)});
const req=(path="/mcp",headers={})=>new Request("https://example.invalid"+path,{method:"POST",headers,body:"{}"});
test("closed default, malformed times, wrong gate and invalid absolute windows",async()=>{
 let n=0;const w=discovery({fetch(){n++;return new Response("ok")}},()=>now);
 for(const e of [{},{...env(),TETHER_DISCOVERY_GATE:"open-native-tether-gateway"},...['',null,now,'NaN'].map(v=>({...env(),TETHER_DISCOVERY_EXPIRES_AT:v})),{...env(),TETHER_DISCOVERY_STARTS_AT:String(now+1)},{...env(),TETHER_DISCOVERY_EXPIRES_AT:String(now)},{...env(),TETHER_DISCOVERY_EXPIRES_AT:String(now+300001)}]) assert.equal((await w.fetch(req(),e)).status,404);
 assert.equal(n,0);assert.equal((await w.fetch(req(),env())).status,200);assert.equal(n,1);
});
test("register, probes, query, aliases and unrelated paths never reach upstream",async()=>{
 let n=0;const w=discovery({fetch(){n++;return new Response("ok")}},()=>now);
 for(const p of ['/tether-agent/v1/register','/tether-probe/v1/echo','/mcp/','/mcp?','/mcp?x=1','/%6dcp','/other']) assert.equal((await w.fetch(req(p),env())).status,404);
 assert.equal(n,0);
});
test("absolute expiry does not renew and late replies are discarded",async()=>{
 let t=now;const w=discovery({fetch(){t=now+300000;return new Response("late")}},()=>t);
 assert.equal((await w.fetch(req(),env())).status,404);
 assert.equal((await w.fetch(req(),env())).status,404);
});
test("native boundary retains Origin refusal, assertion requirement and header filtering",async()=>{
 let calls=[];const native=createNativeWorker({Response,setTimeout,clearTimeout,newPair(){throw Error('no sockets')}});
 const e={...env(),TETHER_NATIVE_VPC:{async fetch(url,init){calls.push({url,init});return new Response('{}',{headers:{'Content-Type':'application/json'}})}}};
 const w=discovery(native,()=>now);
 assert.equal((await w.fetch(req(),e)).status,403);
 assert.equal((await w.fetch(req('/mcp',{'Cf-Access-Jwt-Assertion':'synthetic','Origin':'https://example.invalid'}),e)).status,403);
 assert.equal(calls.length,0);
 assert.equal((await w.fetch(req('/mcp',{'Cf-Access-Jwt-Assertion':'synthetic','Authorization':'Bearer do-not-forward','Cookie':'do-not-forward','Content-Type':'application/json'}),e)).status,200);
 assert.equal(calls.length,1);assert.equal(calls[0].url,'http://rescue-gateway.internal/mcp');assert.equal(calls[0].init.redirect,'manual');assert.equal(calls[0].init.headers.Authorization,undefined);assert.equal(calls[0].init.headers.Cookie,undefined);
});
