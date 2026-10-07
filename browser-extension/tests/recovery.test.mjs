import { test } from 'node:test';
import assert from 'node:assert/strict';
import { core, fakeApi, setup, handshake, config, serverState, tabsMessages, settle } from './helpers.mjs';
const url='https://meet.google.com/original', glob='*meet.google.com/*';
const recorded={state:'recording',owner:true,recording_active:true,recording_revision:7};
async function started(){const ctx=await setup({api:fakeApi({tabs:[{id:1,url}]})});await handshake(ctx,config(),recorded);return ctx;}
async function restarted(original,changes={}){
  original.app.client.stop();
  const data=structuredClone(original.api.store);
  const ctx=await setup({api:fakeApi({history:false,data,tabs:[{id:88,url}],...changes})});return ctx;
}
async function advance(ctx,ms){
  while(ms>0){
    for(const seq of ctx.app.client.pending.keys())ctx.socket.message({type:'ack',seq});
    ctx.socket.message({type:'ask',what:'tabs',owner:ctx.app.control.data.owned,recording_active:ctx.app.control.data.active,recording_revision:ctx.app.control.data.revision});
    await settle();const step=Math.min(ms,10000);await ctx.timers.advance(step);ms-=step;
  }
}
const commands=ctx=>tabsMessages(ctx.socket).filter(m=>m.command_id);

test('durable browser context contains only source, client and revision; external stop clears it',async()=>{
  const ctx=await started();
  assert.deepEqual(ctx.api.store.recordingRecovery,{schema_version:1,client_id:ctx.app.clientId,source_url:url,source_glob:glob,recording_revision:7});
  await ctx.app.uiMessage({kind:'wait-switch'});
  ctx.socket.message(serverState({state:'idle',owner:false,recording_active:false,recording_revision:8}));await settle();
  assert.equal(ctx.api.store.recordingRecovery,undefined);assert.equal(ctx.app.publicStatus().origin,null);
  assert.equal(ctx.app.publicStatus().switchAt,null);assert(ctx.app.publicStatus().rows.every(r=>r.reason==='Устаревшая'));
});
test('full browser restart waits for server state, restores original source and starts 60 seconds without a controller',async()=>{
  const ctx=await restarted(await started());
  ctx.socket.message(config());await settle();assert.equal(tabsMessages(ctx.socket).length,0);
  assert(ctx.api.store.recordingRecovery);assert.equal(ctx.app.publicStatus().recordingActive,null);
  ctx.socket.message(serverState(recorded));await settle();
  const status=ctx.app.publicStatus();assert.equal(status.switchAt,ctx.app.now()+60000);
  assert.equal(status.restartWaiting,true);assert.equal(status.controller,null);assert.equal(status.origin.url,url);
  assert.equal(status.origin.id,undefined);assert.equal(status.rows[0].reason,'Устаревшая');
  assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[url]);assert.equal(commands(ctx).length,0);
});
test('recovery expiry requests stop after exactly 60 seconds plus delay, without refreshing old pages',async()=>{
  const ctx=await restarted(await started());await handshake(ctx,config(),recorded);
  await advance(ctx,59999);assert.equal(commands(ctx).length,0);
  await advance(ctx,1);assert.equal(ctx.app.publicStatus().switchAt,null);assert.equal(commands(ctx).length,0);
  await advance(ctx,4999);assert.equal(commands(ctx).length,0);await advance(ctx,1);
  assert.equal(commands(ctx).length,1);assert.equal(commands(ctx)[0].urls.at(-1),core.CONTROL_URLS.stop);
  assert.equal(ctx.app.command.automatic,true);assert.equal(ctx.app.command.revision,7);
});
test('full restart recovery uses the locally configured delay after the 60-second transfer window',async()=>{
  const original=await started();original.api.store.settings.stopDelaySeconds=12;
  const ctx=await restarted(original);await handshake(ctx,config({stop_delay_seconds:0}),recorded);
  await advance(ctx,71999);assert.equal(commands(ctx).length,0);await advance(ctx,1);
  assert.equal(commands(ctx).length,1);assert.equal(ctx.app.command.revision,7);
});
test('F5 of a restored page cannot adopt control; explicit selection of it can',async()=>{
  const ctx=await restarted(await started());await handshake(ctx,config(),recorded);
  await ctx.app.tabEvent({kind:'navigation',id:88,url,at:ctx.app.now()});assert.equal(ctx.app.publicStatus().controller,null);
  assert.equal((await ctx.app.uiMessage({kind:'select-controller'})).ok,true);
  assert.equal(ctx.app.publicStatus().controller.id,88);assert.equal(ctx.app.publicStatus().switchAt,null);
  await advance(ctx,65000);assert.equal(commands(ctx).length,0);
});
test('a fresh same-GLOB page adopts restored recording immediately while old duplicates remain stale',async()=>{
  const ctx=await restarted(await started(),{detectionDelaySeconds:3});await handshake(ctx,config(),recorded);
  const next=url+'?fresh';ctx.api.tabs.push({id:89,url:next});await ctx.app.tabEvent({kind:'created',id:89,url:next,at:ctx.app.now()});
  assert.equal(ctx.app.publicStatus().controller.id,89);assert.equal(ctx.app.publicStatus().switchAt,null);
  assert.equal(ctx.app.publicStatus().notice,'Запись продолжена: новая вкладка по тому же GLOB открыта повторно.');
  assert.equal(ctx.app.publicStatus().origin.url,url);assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[url]);
});
test('restored recording can link a nonmatching active tab and keeps original source through navigation',async()=>{
  const ctx=await restarted(await started(),{tabs:[{id:99,url:'https://docs.example/',active:true}]});await handshake(ctx,config(),recorded);
  await ctx.app.uiMessage({kind:'select-controller'});ctx.api.tabs[0].url='https://docs.example/next';
  await ctx.app.tabEvent({kind:'navigation',id:99,url:ctx.api.tabs[0].url,at:ctx.app.now()});
  assert.equal(ctx.app.publicStatus().controller.url,'https://docs.example/next');assert.deepEqual(tabsMessages(ctx.socket).at(-1).urls,[url]);
  ctx.api.tabs=[];await ctx.app.tabEvent({kind:'removed',id:99,at:ctx.app.now()});assert.equal(ctx.app.publicStatus().stopAt,ctx.app.now()+5000);
});
test('worker recreation and transport reconnect preserve absolute recovery deadlines',async()=>{
  const ctx=await restarted(await started());await handshake(ctx,config(),recorded);const deadline=ctx.app.publicStatus().switchAt;
  await advance(ctx,10000);const at=ctx.app.now();ctx.app.client.stop();
  const worker=await setup({api:ctx.api,now:()=>at});await handshake(worker,config(),recorded);
  assert.equal(worker.app.publicStatus().switchAt,deadline);assert.equal(worker.app.publicStatus().controller,null);
  worker.socket.close();await settle();await worker.app.alarm();const socket=worker.sockets.at(-1);socket.open();
  await handshake({...worker,socket},config(),recorded);assert.equal(worker.app.publicStatus().switchAt,deadline);
});
test('worker eviction during the first restart handshake does not lose the pending durable context',async()=>{
  const ctx=await restarted(await started());ctx.socket.message(config());await settle();ctx.app.client.stop();
  const worker=await setup({api:ctx.api});await handshake(worker,config(),recorded);
  assert.equal(worker.app.publicStatus().restartWaiting,true);assert.equal(worker.app.publicStatus().origin.url,url);
});
test('restart rejects ended, changed and unowned recordings without binding old pages or stopping another meeting',async()=>{
  for(const state of [
    {state:'idle',owner:false,recording_active:false,recording_revision:8},
    {...recorded,recording_revision:9},{...recorded,owner:false}
  ]){
    const ctx=await restarted(await started());await handshake(ctx,config(),state);
    assert.equal(ctx.app.publicStatus().origin,null);assert.equal(ctx.app.publicStatus().switchAt,null);
    assert.equal(ctx.api.store.recordingRecovery,undefined);await advance(ctx,65000);assert.equal(commands(ctx).length,0);
  }
});
test('foreign or corrupt restart context never binds an active recording',async()=>{
  for(const corrupt of [{client_id:'another-profile'},{schema_version:99},{source_url:core.CONTROL_URLS.start},{source_glob:null},{source_url:'not a URL'},{source_glob:'https://different.example/*'}]){
    const original=await started();Object.assign(original.api.store.recordingRecovery,corrupt);
    const ctx=await restarted(original);await handshake(ctx,config(),recorded);
    assert.equal(ctx.app.publicStatus().origin,null);assert.equal(ctx.api.store.recordingRecovery,undefined);
  }
});
test('ownership release and disabling integration cancel local timers without ending global recording',async()=>{
  for(const disabled of [false,true]){
    const ctx=await started();await ctx.app.uiMessage({kind:'wait-switch'});
    if(disabled)ctx.socket.message(config({enabled:false}));
    else ctx.socket.message(serverState({...recorded,owner:false,state:'idle'}));
    await settle();assert.equal(ctx.app.publicStatus().origin,null);assert.equal(ctx.app.publicStatus().switchAt,null);
    assert.equal(ctx.app.publicStatus().recordingActive,true);assert.equal(ctx.api.store.recordingRecovery,undefined);
    await ctx.timers.advance(65000);assert.equal(commands(ctx).length,0);
  }
});
test('application and toolbar manual recordings do not create a restart context',async()=>{
  const ctx=await setup({api:fakeApi({tabs:[{id:1,url}]})});await handshake(ctx);
  await ctx.app.uiMessage({kind:'manual-start'});const command=commands(ctx)[0];
  ctx.socket.message(serverState({state:'idle',owner:false,recording_active:true,recording_revision:1}));
  ctx.socket.message({type:'ack',seq:command.seq,command_id:command.command_id,command_result:'applied'});await settle();
  assert.equal(ctx.app.publicStatus().origin,null);assert.equal(ctx.api.store.recordingRecovery,undefined);
  const next=await restarted(ctx);await handshake(next,config(),{state:'idle',owner:false,recording_active:true,recording_revision:1});
  assert.equal(next.app.publicStatus().switchAt,null);assert.equal(next.app.publicStatus().recordingActive,true);
});
test('automatic stop waiting for ack cannot replay against a different recording after reconnect',async()=>{
  const ctx=await started();ctx.api.tabs=[];await ctx.app.tabEvent({kind:'removed',id:1});await advance(ctx,5000);
  assert.equal(commands(ctx).length,1);ctx.socket.close();await settle();await ctx.app.alarm();const socket=ctx.sockets.at(-1);socket.open();
  await handshake({...ctx,socket},config(),{state:'idle',owner:false,recording_active:true,recording_revision:9});
  assert.equal(ctx.app.command,null);assert.equal(tabsMessages(socket).filter(m=>m.command_id).length,0);
});
test('offline expiry retains absolute deadline and stops only after fresh same-recording state',async()=>{
  const ctx=await restarted(await started());await handshake(ctx,config(),recorded);const deadline=ctx.app.publicStatus().switchAt;
  ctx.app.client.stop();ctx.app.recordingKnown=false;await ctx.timers.advance(66000);
  assert.equal(commands(ctx).length,0);assert.equal(ctx.app.control.data.switchAt,deadline);
  await ctx.app.reload();const socket=ctx.sockets.at(-1);socket.open();await handshake({...ctx,socket},config(),recorded);
  assert.equal(ctx.app.control.data.stopAt,deadline+5000);
  assert.equal(tabsMessages(socket).filter(m=>m.command_id).length,1);
});
test('changing connection settings or clearing settings deletes durable context and pending commands',async()=>{
  for(const clear of [false,true]){
    const ctx=await started();await ctx.app.uiMessage({kind:'manual-stop'});
    if(clear)await ctx.app.store.clear();else await ctx.app.store.save(52500,'lma_pl_another');
    await ctx.app.reload();assert.equal(ctx.api.store.recordingRecovery,undefined);assert.equal(ctx.app.command,null);
    assert.equal(ctx.app.publicStatus().origin,null);assert.equal(ctx.api.session.pendingCommand,null);
  }
});
test('unsupported protocol response and close code halt retries persistently until connection settings change',async()=>{
  for(const close of [false,true]){
    const ctx=await setup();if(close)ctx.socket.close(1002);else ctx.socket.message({type:'error',error:'unsupported_protocol'});
    await settle();assert.equal(ctx.app.status,'unsupported_protocol');assert.equal(ctx.api.store.protocolBlocked,true);
    await ctx.timers.advance(60000);await ctx.app.reload();assert.equal(ctx.sockets.length,1);assert.equal(ctx.app.status,'unsupported_protocol');
    await ctx.app.store.save(52500,'lma_pl_another');await ctx.app.reload();assert.equal(ctx.api.store.protocolBlocked,undefined);assert.equal(ctx.sockets.length,2);
  }
});
test('legacy states and impossible confirmation ownership are rejected',()=>{
  for(const state of ['blocked','detached','declined','stop_pending'])assert.throws(()=>core.validateMessage({...serverState(recorded),state}));
  assert.throws(()=>core.validateMessage({...serverState(recorded),state:'awaiting_confirmation',owner:false,prompt_id:'p'}));
});
