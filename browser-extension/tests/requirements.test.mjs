import test from 'node:test';
import assert from 'node:assert/strict';
import {setup,fakeApi,handshake,config,serverState,settle,tabsMessages,core,FakeTimers,FakeSocket} from './helpers.mjs';
const a='https://meet.google.com/abc',b='https://meet.google.com/xyz';
async function active(options={}){const ctx=await setup({api:fakeApi({tabs:[{id:1,url:a},{id:2,url:b}],...options})});await handshake(ctx,config(),{state:'recording',owner:true});return ctx;}
async function remove(ctx,id){ctx.api.tabs=ctx.api.tabs.filter(tab=>tab.id!==id);await ctx.app.tabEvent({kind:'removed',id});}
async function open(ctx,id,url=a){ctx.api.tabs.push({id,url});await ctx.app.tabEvent({kind:'created',id,url,at:ctx.app.now()});}
const commands=ctx=>tabsMessages(ctx.socket).filter(message=>message.command_id);
async function elapsed(ctx,ms){
  let remaining=ms;
  while(remaining>0){
    for(const seq of [...ctx.app.client.pending.keys()])ctx.socket.message({type:'ack',seq});
    ctx.socket.message({type:'ask',what:'tabs',owner:ctx.app.state.owner,recording_active:ctx.app.control.data.active,recording_revision:ctx.app.control.data.revision});
    await settle();const step=Math.min(10000,remaining);await ctx.timers.advance(step);remaining-=step;
  }
}
test('old matching duplicates never take control when source closes',async()=>{
  const ctx=await active();await remove(ctx,1);assert(ctx.app.control.data.stopAt);assert.equal(ctx.app.control.data.controller.id,1);
  await ctx.timers.advance(5000);assert(commands(ctx).at(-1).urls.includes(core.CONTROL_URLS.stop));
});
test('fresh duplicate before stop deadline resumes with the exact requested text and original URL',async()=>{
  const ctx=await active({detectionDelaySeconds:3});await remove(ctx,1);await ctx.timers.advance(2000);await open(ctx,3,b);
  assert.equal(ctx.app.control.data.controller.id,3);assert.equal(ctx.app.control.data.stopAt,undefined);
  assert.equal(ctx.app.publicStatus().notice,'Запись продолжена: новая вкладка по тому же GLOB открыта повторно.');
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[a]);await ctx.timers.advance(4000);assert.equal(commands(ctx).length,0);
});
test('fresh page of a different GLOB does not continue the original recording',async()=>{
  const ctx=await active();ctx.socket.message(config({patterns:['*meet.google.com/*','*zoom.us/*']}));await settle();
  await remove(ctx,1);await open(ctx,3,'https://zoom.us/new');assert.equal(ctx.app.control.data.controller.id,1);
  await ctx.timers.advance(5000);assert.equal(commands(ctx).length,1);
});
test('same-URL reload cannot rescue a previously old matching tab',async()=>{
  const ctx=await active();await remove(ctx,1);await ctx.app.tabEvent({kind:'navigation',id:2,url:b,at:ctx.app.now()});
  assert.equal(ctx.app.control.data.controller.id,1);await ctx.timers.advance(5000);assert.equal(commands(ctx).length,1);
});
test('a changed matching URL in the same controller tab is a new variant and resumes without delay',async()=>{
  const ctx=await active({detectionDelaySeconds:3});ctx.api.tabs[0].url=b;await ctx.app.tabEvent({kind:'navigation',id:1,url:b,at:ctx.app.now()});
  assert.equal(ctx.app.control.data.controller.url,b);assert.equal(ctx.app.control.data.origin.url,a);assert.equal(ctx.app.control.data.stopAt,undefined);
});
test('decline excludes all open pages, a fresh same-GLOB tab is eligible after three seconds',async()=>{
  const ctx=await setup({api:fakeApi({detectionDelaySeconds:3,tabs:[{id:1,url:a},{id:2,url:b}]})});
  await handshake(ctx,config({mode:'notify'}),{state:'awaiting_confirmation',owner:true,prompt_id:'p1'});
  assert.equal((await ctx.app.answer('decline','p1')).ok,true);assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[]);
  await open(ctx,3,a);assert.equal(ctx.app.pendingCount,1);await ctx.timers.advance(3000);assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[a]);
});
test('server end makes all current pages stale, F5 stays old, changed URL is fresh',async()=>{
  const ctx=await active();ctx.socket.message(serverState({state:'idle',owner:false,recording_revision:2}));await settle();
  assert.equal(ctx.app.publicStatus().recordingActive,false);assert(ctx.app.publicStatus().rows.every(row=>row.reason==='Устаревшая'));
  await ctx.app.tabEvent({kind:'navigation',id:1,url:a});assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[]);
  ctx.api.tabs[0].url=a+'?new';await ctx.app.tabEvent({kind:'navigation',id:1,url:ctx.api.tabs[0].url,at:ctx.app.now()});assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[a+'?new']);
});
test('60-second hold resets and closing the source waits 60 seconds then the normal stop delay',async()=>{
  const ctx=await active();await ctx.app.uiMessage({kind:'wait-switch'});const first=ctx.app.control.data.switchAt;
  await ctx.timers.advance(10000);await ctx.app.uiMessage({kind:'wait-switch'});assert.equal(ctx.app.control.data.switchAt,first+10000);
  await remove(ctx,1);await elapsed(ctx,59999);assert.equal(commands(ctx).length,0);
  await ctx.timers.advance(1);assert(ctx.app.control.data.stopAt);await ctx.timers.advance(5000);assert.equal(commands(ctx).length,1);
});
test('switch expiry with source still open continues recording',async()=>{
  const ctx=await active();await ctx.app.uiMessage({kind:'wait-switch'});await elapsed(ctx,60000);
  assert.equal(ctx.app.control.data.switchAt,undefined);assert.equal(ctx.app.control.data.stopAt,undefined);assert.equal(commands(ctx).length,0);
});
test('an old matching tab present when switching starts is not selected automatically',async()=>{
  const ctx=await active();await ctx.app.uiMessage({kind:'wait-switch'});await ctx.app.snapshot();assert.equal(ctx.app.control.data.controller.id,1);
});
test('a new matching page during hold takes control and keeps initial URL',async()=>{
  const ctx=await active();await ctx.app.uiMessage({kind:'wait-switch'});await open(ctx,3,b);
  assert.equal(ctx.app.control.data.controller.id,3);assert.equal(ctx.app.control.data.switchAt,undefined);assert.equal(ctx.app.control.data.origin.url,a);
});
test('any active tab may control recording, its navigation is allowed, closing it starts stop delay',async()=>{
  const ctx=await active();ctx.api.tabs.push({id:9,url:'https://docs.example.org/',active:true});await ctx.app.uiMessage({kind:'wait-switch'});
  assert.equal((await ctx.app.uiMessage({kind:'select-controller'})).ok,true);assert.equal(ctx.app.control.data.controller.id,9);
  await remove(ctx,1);assert.equal(ctx.app.control.data.stopAt,undefined);
  ctx.api.tabs.find(tab=>tab.id===9).url='https://docs.example.org/next';await ctx.app.tabEvent({kind:'navigation',id:9,url:'https://docs.example.org/next'});
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[a]);await remove(ctx,9);assert(ctx.app.control.data.stopAt);
});
test('worker recreation restores source, controller and remaining switching deadline',async()=>{
  const ctx=await active();await ctx.app.uiMessage({kind:'wait-switch'});await ctx.timers.advance(10000);ctx.app.client.stop();
  const sockets=[],timers=new FakeTimers();const app=new core.ExtensionApp(ctx.api,{timers,now:()=>ctx.app.now()+timers.now,socketFactory:url=>{const socket=new FakeSocket(url);sockets.push(socket);return socket;}});
  await app.reload();sockets[0].open();await handshake({app,socket:sockets[0]},config(),{state:'recording',owner:true});
  assert.deepEqual(app.publicStatus().origin,ctx.app.publicStatus().origin);assert.equal(app.publicStatus().switchAt,ctx.app.publicStatus().switchAt);
});
test('manual command remains pending until command ack; status is never changed optimistically',async()=>{
  const ctx=await setup();await handshake(ctx);assert.equal((await ctx.app.uiMessage({kind:'manual-start'})).ok,true);
  assert.equal(ctx.app.publicStatus().recordingActive,false);const command=commands(ctx)[0];assert.equal(command.urls.at(-1),core.CONTROL_URLS.start);
  ctx.socket.message({type:'ack',seq:command.seq});await settle();assert(ctx.app.command);
  ctx.socket.message(serverState({state:'recording',owner:false,recording_revision:1}));ctx.socket.message({type:'ack',seq:command.seq,command_id:command.command_id,command_result:'applied'});await settle();
  assert.equal(ctx.app.publicStatus().recordingActive,true);assert.equal(ctx.app.publicStatus().origin,null);assert.equal(ctx.app.command,null);
});
test('commands are not repeated on ask or UI polls and reconnect retries the same command ID',async()=>{
  const ctx=await setup();await handshake(ctx);await ctx.app.uiMessage({kind:'manual-start'});const id=commands(ctx)[0].command_id;
  ctx.socket.message({type:'ask',what:'tabs',owner:ctx.app.state.owner,recording_active:false,recording_revision:0});await settle();await ctx.app.uiMessage({kind:'status'});assert.equal(commands(ctx).length,1);
  ctx.socket.close();await settle();await ctx.app.alarm();const socket=ctx.sockets.at(-1);socket.open();await handshake({...ctx,socket});
  assert.equal(tabsMessages(socket).find(message=>message.command_id).command_id,id);
});
test('connection loss preserves remembered recording but public state is unknown and controls disabled',async()=>{
  const ctx=await active();ctx.socket.close();await settle();assert.equal(ctx.app.control.data.active,true);
  assert.equal(ctx.app.publicStatus().recordingActive,null);assert.equal(ctx.app.publicStatus().canControl,false);assert.equal((await ctx.app.uiMessage({kind:'manual-stop'})).ok,false);
});
test('old server cannot receive commands or fresh-page snapshots',async()=>{
  const ctx=await setup({api:fakeApi({tabs:[{id:1,url:a}]})});await handshake(ctx,config({protocol_version:3,capabilities:[]}));
  assert.equal(ctx.app.publicStatus().compatible,false);assert.equal((await ctx.app.uiMessage({kind:'manual-start'})).ok,false);assert.equal(tabsMessages(ctx.socket).length,0);
});
test('reserved command URLs opened as real tabs never become commands or candidates',async()=>{
  const ctx=await setup({api:fakeApi({tabs:[{id:1,url:core.CONTROL_URLS.stop}]})});await handshake(ctx,config({patterns:['*']}));
  assert.equal(ctx.app.publicStatus().openCount,0);assert(tabsMessages(ctx.socket).every(message=>message.urls.length===0));
});
test('list includes duplicate URLs as separate tabs; close-all only closes matching tabs',async()=>{
  const ctx=await setup({api:fakeApi({tabs:[{id:1,url:a},{id:2,url:a},{id:3,url:'https://example.org/'}]})});await handshake(ctx);
  assert.equal(ctx.app.publicStatus().rows.length,2);await ctx.app.uiMessage({kind:'close-tab',tabId:1});assert.equal(ctx.api.tabs.length,2);
  await ctx.app.uiMessage({kind:'close-all'});assert.deepEqual(ctx.api.tabs.map(tab=>tab.id),[3]);
});
test('globe opens the configured loopback web interface, not a command URL',async()=>{
  const ctx=await setup();await handshake(ctx);await ctx.app.uiMessage({kind:'open-app'});assert.equal(ctx.api.tabs.at(-1).url,'http://127.0.0.1:52469/');
});
test('missing state fields on a v1 server is a protocol error',async()=>{
  const ctx=await setup();ctx.socket.message(config());ctx.socket.message({type:'state',state:'idle',owner:false});await settle();assert.equal(ctx.app.status,'protocol_error');
});
test('rejected timed stop is not retried in a tight loop',async()=>{
  const ctx=await active();await remove(ctx,1);await ctx.timers.advance(5000);const command=commands(ctx)[0];
  ctx.socket.message({type:'ack',seq:command.seq,command_id:command.command_id,command_result:'rejected'});await settle();
  for(let i=0;i<3;i++)await ctx.app.snapshot(true);
  assert.equal(commands(ctx).length,1);assert.equal(ctx.app.command,null);assert(ctx.app.publicStatus().commandError);
});
test('delayed processing of navigation uses the observed event time for continuation',async()=>{
  const ctx=await active({detectionDelaySeconds:3});ctx.api.tabs[0].url=b;
  await ctx.app.tabEvent({kind:'navigation',id:1,url:b,at:ctx.app.now()-10});
  assert.equal(ctx.app.control.data.controller.url,b);assert.equal(ctx.app.control.data.stopAt,undefined);
});
test('missed end/start while offline drops old controller before an expired stop can affect new recording',async()=>{
  const ctx=await active();ctx.socket.close();await settle();await remove(ctx,1);
  const socket=ctx.sockets.at(-1);socket.open();await handshake({...ctx,socket},config(),{state:'recording',owner:false,recording_revision:3});
  assert.equal(ctx.app.control.data.origin,undefined);assert.equal(ctx.app.control.data.stopAt,undefined);
  await ctx.timers.advance(5000);assert(tabsMessages(socket).every(message=>!message.urls.includes(core.CONTROL_URLS.stop)));
});
