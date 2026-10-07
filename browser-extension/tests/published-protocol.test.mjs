import { test } from 'node:test';
import assert from 'node:assert/strict';
import {core,config,fakeApi,setup,handshake,settle,serverState,tabsMessages} from './helpers.mjs';
const url='https://meet.google.com/published';
async function active(stopDelaySeconds=5){
  const ctx=await setup({api:fakeApi({data:{settings:{stopDelaySeconds}},tabs:[{id:1,url}]})});
  await handshake(ctx,config(),{state:'recording',owner:true});return ctx;
}
async function remove(ctx){ctx.api.tabs=[];await ctx.app.tabEvent({kind:'removed',id:1,at:ctx.app.now()});}
const commands=ctx=>tabsMessages(ctx.socket).filter(m=>m.command_id);
async function advance(ctx,ms){while(ms>0){
  for(const seq of ctx.app.client.pending.keys())ctx.socket.message({type:'ack',seq});
  ctx.socket.message({type:'ask',what:'tabs',owner:ctx.app.state.owner,recording_active:ctx.app.control.data.active,recording_revision:ctx.app.control.data.revision});
  await settle();const step=Math.min(ms,10000);await ctx.timers.advance(step);ms-=step;
}}
test('published config omits stop delay; hello and compatibility use exactly protocol 1',async()=>{
  const ctx=await active();assert.equal(ctx.socket.sent[0].protocol_version,1);
  assert.equal('stop_delay_seconds' in ctx.app.config,false);assert.equal(ctx.app.status,'online');assert.equal(ctx.app.compatible,true);
  const old=await setup();await handshake(old,config({protocol_version:4}));assert.equal(old.app.compatible,false);
  assert.equal(tabsMessages(old.socket).length,0);assert.equal((await old.app.uiMessage({kind:'manual-start'})).ok,false);
});
test('old optional stop_delay_seconds in config is ignored, including invalid values',async()=>{
  for(const value of [0,300,'invalid',null]){
    const ctx=await active(17);ctx.socket.message(config({stop_delay_seconds:value}));await settle();await remove(ctx);
    assert.equal(ctx.app.control.data.stopAt,ctx.app.now()+17000);assert.equal(ctx.app.status,'online');
  }
});
test('local stop delay validates 0–300 whole seconds and migrates old settings to five seconds',async()=>{
  for(const value of [-1,301,1.5,'2e1','abc'])assert.throws(()=>core.validateSettings(52469,'lma_pl_test',180,3,value));
  for(const value of [0,5,300])assert.equal(core.validateSettings(52469,'lma_pl_test',180,3,value).stopDelaySeconds,value);
  const api=fakeApi();const store=new core.SettingsStore(api,()=> 'install');assert.equal((await store.load()).settings.stopDelaySeconds,5);
  assert.equal(await store.save(52469,api.store.settings.token,180,0,9),true);
  assert.equal((await store.load()).settings.stopDelaySeconds,9);
});
test('local stop delay zero stops immediately; maximum delay lasts 300 seconds',async()=>{
  const immediate=await active(0);await remove(immediate);assert.equal(commands(immediate).length,1);
  const longest=await active(300);await remove(longest);await advance(longest,299999);assert.equal(commands(longest).length,0);
  await advance(longest,1);assert.equal(commands(longest).length,1);
});
test('local setting edits do not reconnect the socket or extend an existing stop deadline',async()=>{
  const ctx=await active(5);await remove(ctx);const deadline=ctx.app.control.data.stopAt;
  const client=ctx.app.client,generation=client.generation;
  await ctx.app.store.save(52469,ctx.api.store.settings.token,180,0,40);await ctx.app.settingsChanged();
  assert.equal(ctx.app.client,client);assert.equal(ctx.app.client.generation,generation);assert.equal(ctx.app.settings.stopDelaySeconds,40);
  assert.equal(ctx.app.control.data.stopAt,deadline);await advance(ctx,5000);assert.equal(commands(ctx).length,1);
});
test('local stop delay never ends an application or another profile recording',async()=>{
  for(const owner of [false]){
    const ctx=await setup({api:fakeApi({data:{settings:{stopDelaySeconds:0}},tabs:[{id:1,url}]})});
    await handshake(ctx,config(),{state:'idle',owner,recording_active:true,recording_revision:1});await remove(ctx);await advance(ctx,300000);
    assert.equal(commands(ctx).length,0);assert.equal(ctx.app.publicStatus().recordingActive,true);
  }
});
test('an observed completion sends an ordinary empty snapshot before a later fresh page can launch',async()=>{
  const ctx=await active();const before=tabsMessages(ctx.socket).length;
  ctx.socket.message(serverState({state:'idle',owner:false,recording_active:false,recording_revision:2}));await settle();
  const after=tabsMessages(ctx.socket).slice(before);assert(after.length);assert.deepEqual(after[0].urls,[]);assert.equal(after[0].command_id,undefined);
  assert.equal(ctx.api.store.recordingRecovery,undefined);
  const fresh=url+'?fresh';ctx.api.tabs.push({id:2,url:fresh});await ctx.app.tabEvent({kind:'created',id:2,url:fresh,at:ctx.app.now()});
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[fresh]);
});
test('a tab discovered in the completion scan is also stale and cannot skip the empty snapshot',async()=>{
  const ctx=await active();ctx.api.tabs.push({id:2,url:url+'?unseen'});const before=tabsMessages(ctx.socket).length;
  ctx.socket.message(serverState({state:'idle',owner:false,recording_active:false,recording_revision:2}));await settle();
  assert(tabsMessages(ctx.socket).slice(before).every(m=>m.urls.length===0));assert(ctx.app.publicStatus().rows.every(r=>r.reason==='Устаревшая'));
});
test('minimum N=2 transport recovery precedes 4*poll after silence for all supported poll periods, including a failed first attempt',async()=>{
  for(let poll=10;poll<=25;poll++){
    const ctx=await active();ctx.socket.message(config({poll_interval_seconds:poll}));await settle();
    for(const seq of ctx.app.client.pending.keys())ctx.socket.message({type:'ack',seq});await settle();
    const start=ctx.app.now(),threshold=(3*poll+5)*1000,stopAt=start+4*poll*1000;
    await ctx.timers.advance(threshold-1);assert.equal(ctx.socket.readyState,1);await ctx.timers.advance(1);
    assert.equal(ctx.app.status,'offline');await ctx.timers.advance(1000);
    // Let the first replacement socket fail without opening, then verify a prompt retry rather than growing backoff.
    const connectTask=ctx.timers.tasks.get(ctx.app.client.connectTimer);assert(connectTask);
    await ctx.timers.advance(connectTask.at-ctx.timers.now+1000);
    const socket=ctx.sockets.at(-1);assert.equal(socket.readyState,0);socket.open();
    await handshake({...ctx,socket},config({poll_interval_seconds:poll}),{state:'recording',owner:true});
    assert.equal(ctx.app.status,'online');assert(ctx.app.now()<stopAt,`poll=${poll}`);assert.equal(ctx.app.publicStatus().origin.url,url);
    assert.equal(ctx.app.client.pollSeconds,poll);ctx.app.client.stop();
  }
});
test('protocol rejection from a previous draft cannot block the new wire version after upgrade',async()=>{
  const api=fakeApi({data:{protocolBlocked:true}});const ctx=await setup({api});assert(ctx.socket);
  assert.equal(api.store.protocolBlocked,undefined);ctx.socket.message({type:'error',error:'unsupported_protocol'});await settle();
  assert.equal(api.store.protocolBlockedVersion,1);await ctx.app.reload();assert.equal(ctx.app.status,'unsupported_protocol');
});
