import { test } from "node:test";
import assert from "node:assert/strict";
import { createBoundedPilot } from "./bounded-pilot.mjs";
import { GATE_VALUE, SUBPROTOCOL } from "./worker.mjs";
const START = 1800000000000;
class Socket {
  constructor() { this.listeners = {}; this.closed = 0; this.sent = []; }
  accept() { this.accepted = true; }
  close() { this.closed++; }
  send(v) { this.sent.push(v); }
  addEventListener(t,f) { (this.listeners[t] ??= []).push(f); }
  emit(t,e) { for (const f of this.listeners[t] ?? []) f(e); }
}
class Reply {
  constructor(body, init={}) { Object.assign(this, {body,status:200,headers:new Headers()},init); this.headers=new Headers(init.headers); }
}
function fixture(ms=1000) {
  let now=START, id=0; const timers=new Map(), pairs=[], calls=[];
  const rt={now:()=>now, Response:Reply,
    newPair:()=>{const p=[new Socket(),new Socket()];pairs.push(p);return p;},
    setTimeout:(fn,delay)=>{timers.set(++id,{fn,delay});return id;},
    clearTimeout:i=>timers.delete(i)};
  const env={TETHER_NATIVE_GATE:GATE_VALUE,TETHER_NATIVE_STARTS:String(START),
    TETHER_NATIVE_EXPIRES:String(START+ms),TETHER_NATIVE_VPC:{fetch:async(...a)=>{calls.push(a);return new Response("{}",{headers:{"Content-Type":"application/json"}});}}};
  return {rt,env,calls,timers,pairs,worker:createBoundedPilot(rt),setNow:v=>now=v,
    fireDelay:delay=>{for(const [i,t] of [...timers])if(t.delay===delay){timers.delete(i);t.fn();}}};
}
const req=(path="/mcp",body=null)=>({url:"https://synthetic.invalid"+path,method:"POST",
  headers:new Headers({"Cf-Access-Jwt-Assertion":"synthetic","Content-Type":"application/json"}),body});
const wsreq=()=>({url:"https://synthetic.invalid/tether-agent/v1/register",method:"GET",
  headers:new Headers({Upgrade:"websocket","Sec-WebSocket-Protocol":SUBPROTOCOL}),body:null});
const upgrade=s=>({status:101,webSocket:s,body:null,headers:new Headers({"Sec-WebSocket-Protocol":SUBPROTOCOL})});
const tick=()=>new Promise(r=>setImmediate(r));

test("closed default and malformed windows never forward",async()=>{
 for(const change of [{},{TETHER_NATIVE_STARTS:START},{TETHER_NATIVE_STARTS:"180000000000"},
 {TETHER_NATIVE_EXPIRES:String(START)},{TETHER_NATIVE_EXPIRES:String(START+300001)},
 {TETHER_NATIVE_EXPIRES:"1.800000000001e12"},{TETHER_NATIVE_GATE:"unknown"}]){
  const f=fixture();const env=Object.keys(change).length?{...f.env,...change}:undefined;
  assert.equal((await f.worker.fetch(req(),env)).status,404);assert.equal(f.calls.length,0);assert.equal(f.timers.size,0);
 }
});
test("exact max window accepted; before and at deadline refused",async()=>{
 const f=fixture(300000);f.setNow(START-1);assert.equal((await f.worker.fetch(req(),f.env)).status,404);
 f.setNow(START);assert.equal((await f.worker.fetch(req(),f.env)).status,200);
 f.setNow(START+300000);assert.equal((await f.worker.fetch(req(),f.env)).status,404);
 assert.equal(f.calls.length,1);
});
test("exact paths inherited; aliases queries other routes never forwarded",async()=>{
 const f=fixture();for(const p of ["/register","/mcp/","/mcp?x=1","/admin","/%6dcp"]){
  assert.notEqual((await f.worker.fetch(req(p),f.env)).status,200);
 }assert.equal(f.calls.length,0);assert.equal(f.timers.size,0);
});
test("normal HTTP releases all owned timers",async()=>{
 const f=fixture();assert.equal((await f.worker.fetch(req(),f.env)).status,200);
 assert.equal(f.calls.length,1);assert.equal(f.timers.size,0);
});
test("window mutation permanently refuses, including rollback to original",async()=>{
 const f=fixture();await f.worker.fetch(req(),f.env);
 assert.equal((await f.worker.fetch(req(),{...f.env,TETHER_NATIVE_EXPIRES:String(START+2000)})).status,404);
 assert.equal((await f.worker.fetch(req(),f.env)).status,404);assert.equal(f.calls.length,1);
});
test("clock backward and noninteger clock are fail closed",async()=>{
 for(const n of [START-1,NaN,Infinity,START+0.5]){
  const f=fixture();await f.worker.fetch(req(),f.env);f.setNow(n);
  assert.equal((await f.worker.fetch(req(),f.env)).status,404);f.setNow(START+1);
  assert.equal((await f.worker.fetch(req(),f.env)).status,404);
 }
});
test("deadline while request body is pending cancels it and never fetches",async()=>{
 const f=fixture();let canceled=false;
 const body=new ReadableStream({pull(){},cancel(){canceled=true;}},{highWaterMark:0});
 const pending=f.worker.fetch(req("/mcp",body),f.env);await tick();
 f.setNow(START+1000);f.fireDelay(1000);
 assert.equal((await pending).status,404);assert.equal(canceled,true);assert.equal(f.calls.length,0);
});
test("late HTTP response canceled and not returned",async()=>{
 const f=fixture();let resolve,canceled=false;
 f.env.TETHER_NATIVE_VPC.fetch=()=>new Promise(r=>resolve=r);
 const p=f.worker.fetch(req(),f.env);await tick();f.setNow(START+1000);
 const stream=new ReadableStream({cancel(){canceled=true;}},{highWaterMark:0});
 resolve(new Response(stream,{headers:{"Content-Type":"application/json"}}));
 assert.equal((await p).status,404);assert.equal(canceled,true);assert.equal(f.timers.size,0);
});
test("late upgraded response accepted only to close without client pair",async()=>{
 const f=fixture();let resolve;const upstream=new Socket();
 f.env.TETHER_NATIVE_VPC.fetch=()=>new Promise(r=>resolve=r);
 const p=f.worker.fetch(wsreq(),f.env);await tick();f.setNow(START+1000);resolve(upgrade(upstream));
 assert.equal((await p).status,404);assert.ok(upstream.closed);assert.equal(f.pairs.length,0);
});
test("existing WebSocket timer is capped to remaining absolute window",async()=>{
 const f=fixture();const upstream=new Socket();f.env.TETHER_NATIVE_VPC.fetch=async()=>upgrade(upstream);
 f.setNow(START+800);assert.equal((await f.worker.fetch(wsreq(),f.env)).status,101);
 assert.ok([...f.timers.values()].every(t=>t.delay<=200));f.setNow(START+1000);f.fireDelay(200);
 assert.ok(upstream.closed);assert.ok(f.pairs[0][1].closed);assert.equal(f.timers.size,0);
});
test("expired message is not relayed even if timer dispatch is delayed",async()=>{
 const f=fixture();const upstream=new Socket();f.env.TETHER_NATIVE_VPC.fetch=async()=>upgrade(upstream);
 await f.worker.fetch(wsreq(),f.env);f.setNow(START+1000);
 upstream.emit("message",{data:"late"});assert.equal(f.pairs[0][1].sent.length,0);
 assert.ok(upstream.closed);assert.ok(f.pairs[0][1].closed);
});
test("clock rollback during existing relay closes both ends",async()=>{
 const f=fixture();const upstream=new Socket();f.env.TETHER_NATIVE_VPC.fetch=async()=>upgrade(upstream);
 await f.worker.fetch(wsreq(),f.env);f.setNow(START-1);upstream.emit("message",{data:"late"});
 assert.equal(f.pairs[0][1].sent.length,0);assert.ok(upstream.closed);assert.ok(f.pairs[0][1].closed);
});
test("closed gate request closes previous owned relay",async()=>{
 const f=fixture();const upstream=new Socket();f.env.TETHER_NATIVE_VPC.fetch=async()=>upgrade(upstream);
 await f.worker.fetch(wsreq(),f.env);assert.equal((await f.worker.fetch(req(),{...f.env,TETHER_NATIVE_GATE:"closed"})).status,404);
 assert.ok(upstream.closed);assert.ok(f.pairs[0][1].closed);
});
test("expired timer cannot be reopened by a wallclock before end",async()=>{
 const f=fixture();const upstream=new Socket();f.env.TETHER_NATIVE_VPC.fetch=async()=>upgrade(upstream);
 await f.worker.fetch(wsreq(),f.env);f.fireDelay(1000);
 assert.equal((await f.worker.fetch(req(),f.env)).status,404);
});

test("upstream ignores expiry abort; late body is discarded after client settled",async()=>{
 const f=fixture();let resolve,canceled=false,signal;
 f.env.TETHER_NATIVE_VPC.fetch=(_u,init)=>{signal=init.signal;return new Promise(r=>resolve=r);};
 const p=f.worker.fetch(req(),f.env);await tick();f.setNow(START+1000);f.fireDelay(1000);
 assert.equal((await p).status,404);assert.equal(signal.aborted,true);
 resolve(new Response(new ReadableStream({cancel(){canceled=true;}},{highWaterMark:0}),{headers:{"Content-Type":"application/json"}}));
 await tick();assert.equal(canceled,true);assert.equal(f.timers.size,0);
});
test("upstream ignores expiry abort; late upgrade never relays",async()=>{
 const f=fixture();let resolve;const upstream=new Socket();
 f.env.TETHER_NATIVE_VPC.fetch=()=>new Promise(r=>resolve=r);
 const p=f.worker.fetch(wsreq(),f.env);await tick();f.setNow(START+1000);f.fireDelay(1000);
 assert.equal((await p).status,404);resolve(upgrade(upstream));await tick();
 assert.ok(upstream.closed);assert.equal(f.pairs.length,0);assert.equal(f.timers.size,0);
});
test("stalled upstream body at absolute end is canceled, never published",async()=>{
 const f=fixture();let canceled=false;
 f.env.TETHER_NATIVE_VPC.fetch=async()=>new Response(new ReadableStream({pull(){},cancel(){canceled=true;}},{highWaterMark:0}),{headers:{"Content-Type":"application/json"}});
 const p=f.worker.fetch(req(),f.env);await tick();f.setNow(START+1000);f.fireDelay(1000);
 assert.equal((await p).status,404);assert.equal(canceled,true);assert.equal(f.timers.size,0);
});
test("clock read failure fails closed",async()=>{
 const f=fixture();f.rt.now=()=>{throw new Error("synthetic");};
 assert.equal((await f.worker.fetch(req(),f.env)).status,404);assert.equal(f.calls.length,0);
});
test("active scope cap refuses before fetch and expiry clears all",async()=>{
 const f=fixture();const sockets=[];
 f.env.TETHER_NATIVE_VPC.fetch=async()=>{const s=new Socket();sockets.push(s);return upgrade(s);};
 for(let i=0;i<32;i++)assert.equal((await f.worker.fetch(wsreq(),f.env)).status,101);
 assert.equal((await f.worker.fetch(wsreq(),f.env)).status,503);assert.equal(sockets.length,32);
 f.setNow(START+1000);f.fireDelay(1000);assert.ok(sockets.every(s=>s.closed));assert.equal(f.timers.size,0);
});
