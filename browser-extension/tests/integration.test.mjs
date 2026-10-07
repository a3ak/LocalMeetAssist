import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createMockServer } from '../scripts/mock-server.mjs';
import { fakeApi, core } from './helpers.mjs';
async function until(predicate, timeout=2500){const started=Date.now();while(!predicate()){if(Date.now()-started>timeout)throw new Error('Integration timeout');await new Promise(resolve=>setTimeout(resolve,10));}}
async function fixture(t,options={}){
  const mock=await createMockServer(options);
  const url=`http://127.0.0.1:${mock.port}/demo`;
  const api=fakeApi({data:{settings:{port:mock.port,token:'lma_pl_DEMO_ONLY',stopDelaySeconds:options.stopDelaySeconds??5}},tabs:[{id:1,url}]});
  const app=new core.ExtensionApp(api);
  t.after(async()=>{app.timers.clearTimeout(app.detectionTimer);app.client?.stop();await mock.close();});
  await app.reload();await until(()=>app.status==='online');return{mock,api,app,url};
}
test('real WebSocket: notify confirm, application stop invalidates pages, fresh navigation asks again',async t=>{
  const {mock,api,app,url}=await fixture(t);await until(()=>app.publicStatus().canAnswer);
  const old=app.publicStatus().promptId;assert.equal((await app.answer('confirm',old)).ok,true);
  await until(()=>app.publicStatus().recordingActive===true);assert.equal(mock.metrics().starts,1);
  mock.manualEnd();await until(()=>app.publicStatus().recordingActive===false&&app.ignoredCount===1);
  await app.snapshot(true);await until(()=>app.client.pending.size===0);assert.equal(mock.metrics().starts,1);
  api.tabs[0].url=url+'?new';await app.tabEvent({kind:'navigation',id:1,url:api.tabs[0].url,at:Date.now()});
  await until(()=>app.publicStatus().canAnswer);assert.notEqual(app.publicStatus().promptId,old);
});
test('real WebSocket: manual toolbar start and stop use only tabs, application start also enables stop',async t=>{
  const {mock,app}=await fixture(t);assert.equal((await app.uiMessage({kind:'manual-start'})).ok,true);
  await until(()=>app.publicStatus().recordingActive===true&&!app.command);assert.equal(app.publicStatus().origin,null);
  assert.equal((await app.uiMessage({kind:'manual-stop'})).ok,true);
  await until(()=>!app.command&&app.publicStatus().recordingActive===false);assert.equal(mock.metrics().stops,1);
  mock.manualStart();await until(()=>app.publicStatus().recordingActive===true);
  await app.uiMessage({kind:'manual-stop'});await until(()=>mock.metrics().stops===2&&!app.command);
});
test('real WebSocket: empty snapshot does not stop recording and disconnect preserves it within grace',async t=>{
  const {mock,api,app}=await fixture(t,{mode:'auto',stopDelaySeconds:5});await until(()=>app.active);
  api.tabs=[];await app.tabEvent({kind:'removed',id:1});assert(app.control.data.stopAt);
  await until(()=>app.client.pending.size===0);assert.equal(mock.metrics().active,true);
  app.client.stop();await new Promise(resolve=>setTimeout(resolve,30));assert.equal(mock.metrics().active,true);
});
test('real WebSocket: missing browser owner stops only after grace, while another browser observes the transition',async t=>{
  let clock=Date.now();const {mock,app}=await fixture(t,{mode:'auto',now:()=>clock,graceMilliseconds:1000});await until(()=>app.active);
  const api=fakeApi({data:{installId:'observer',settings:{port:mock.port,token:'lma_pl_DEMO_ONLY'}}});
  const observer=new core.ExtensionApp(api);t.after(()=>observer.client?.stop());await observer.reload();await until(()=>observer.active);
  assert.equal(observer.publicStatus().owner,false);app.client.stop();await until(()=>mock.metrics().lost===1);
  clock+=999;mock.poll();assert.equal(mock.metrics().active,true);
  clock+=1;mock.poll();await until(()=>observer.publicStatus().recordingActive===false);
  assert.equal(mock.metrics().state,'idle');assert.equal(mock.metrics().owner,null);assert.equal(mock.metrics().stops,1);
});
test('real WebSocket: application and record_manual recording survive missing browser connections',async t=>{
  let clock=Date.now();const {mock,app}=await fixture(t,{now:()=>clock,graceMilliseconds:1000});
  await app.uiMessage({kind:'manual-start'});await until(()=>app.active&&!app.command);
  assert.equal(mock.metrics().owner,null);assert.equal(mock.metrics().state,'idle');
  app.client.stop();clock+=2000;mock.poll();assert.equal(mock.metrics().active,true);assert.equal(mock.metrics().stops,0);
});
test('real WebSocket: full browser restart restores same client session and enters recovery with all restored pages old',async t=>{
  const {mock,api,app,url}=await fixture(t,{mode:'auto'});await until(()=>app.active&&!!api.store.recordingRecovery);
  app.client.stop();await until(()=>mock.metrics().lost===1);
  const restartedApi=fakeApi({history:false,data:structuredClone(api.store),tabs:[{id:200,url}]});
  const restarted=new core.ExtensionApp(restartedApi);t.after(()=>{restarted.timers.clearTimeout(restarted.detectionTimer);restarted.client?.stop();});
  await restarted.reload();await until(()=>restarted.status==='online'&&restarted.publicStatus().restartWaiting);
  assert.equal(mock.metrics().lost,0);assert.equal(mock.metrics().starts,1);assert.equal(mock.metrics().stops,0);
  assert.equal(restarted.publicStatus().controller,null);assert.equal(restarted.publicStatus().rows[0].reason,'Устаревшая');
  assert.equal(restarted.publicStatus().origin.url,url);await restarted.uiMessage({kind:'select-controller'});
  assert.equal(restarted.publicStatus().controller.id,200);assert.equal(restarted.publicStatus().switchAt,null);
});
test('real WebSocket: disabling integration releases session without stopping audio and cancels local control',async t=>{
  const {mock,api,app}=await fixture(t,{mode:'auto'});await until(()=>app.active);await app.uiMessage({kind:'wait-switch'});
  mock.setEnabled(false);await until(()=>app.config.enabled===false&&app.publicStatus().origin===null);
  assert.equal(mock.metrics().active,true);assert.equal(mock.metrics().owner,null);assert.equal(mock.metrics().state,'idle');
  assert.equal(api.store.recordingRecovery,undefined);assert.equal(app.publicStatus().switchAt,null);
  assert.equal((await app.uiMessage({kind:'manual-stop'})).ok,false);
});
test('mock journal: process restart publishes false and counts previous active state exactly once',async t=>{
  const journal={revision:6,active:false};let mock=await createMockServer({journal});
  mock.manualStart();assert.deepEqual(journal,{revision:7,active:true});await mock.close();
  mock=await createMockServer({journal});assert.equal(mock.metrics().revision,8);assert.equal(mock.metrics().active,false);
  const api=fakeApi({data:{settings:{port:mock.port,token:'lma_pl_DEMO_ONLY'}}});const app=new core.ExtensionApp(api);
  await app.reload();await until(()=>app.status==='online');assert.equal(app.publicStatus().recordingActive,false);
  assert.equal(app.control.data.revision,8);app.client.stop();await mock.close();
  const second=await createMockServer({journal});t.after(()=>second.close());assert.equal(second.metrics().revision,8);
});
test('real WebSocket: controller stop delay is executed by extension using stop_manual',async t=>{
  const {mock,api,app}=await fixture(t,{mode:'auto',stopDelaySeconds:0});await until(()=>app.active);
  api.tabs=[];await app.tabEvent({kind:'removed',id:1});
  await until(()=>mock.metrics().stops===1&&!app.command);assert.equal(app.publicStatus().recordingActive,false);
});
test('real WebSocket: decline does not block a fresh tab of the same GLOB',async t=>{
  const {app,api,url}=await fixture(t);await until(()=>app.publicStatus().canAnswer);
  const old=app.publicStatus().promptId;await app.answer('decline',old);
  await until(()=>app.state.state==='idle'&&app.ignoredCount===1);
  api.tabs.push({id:2,url});await app.tabEvent({kind:'created',id:2,url,at:Date.now()});
  await until(()=>app.publicStatus().canAnswer);assert.notEqual(app.publicStatus().promptId,old);
});
test('real WebSocket: reconnect restores source without restarting recording',async t=>{
  const {mock,app}=await fixture(t,{mode:'auto'});await until(()=>app.active);
  const origin={...app.publicStatus().origin};app.client.fail('offline');await app.alarm();
  await until(()=>app.client.ready&&app.active);assert.deepEqual(app.publicStatus().origin,origin);
  assert.equal(mock.metrics().starts,1);assert.equal(mock.metrics().stops,0);
});
test('real WebSocket: heartbeat observes application recording and missing earlier transitions',async t=>{
  const {mock,app}=await fixture(t);await until(()=>app.recordingKnown);
  mock.manualStart();await until(()=>app.active);const rev=app.control.data.revision;
  app.client.fail('offline');mock.manualEnd();mock.manualStart();await app.alarm();
  await until(()=>app.client.ready&&app.active&&app.control.data.revision>rev);
  assert.equal(app.publicStatus().origin,null);assert(app.publicStatus().rows.every(row=>row.reason==='Устаревшая'));
  mock.poll();await until(()=>app.client.pending.size===0);assert(app.lastHeartbeat);
});
test('real WebSocket: a non-owner cannot confirm a prompt but can explicitly stop global recording',async t=>{
  const {mock,app}=await fixture(t);await until(()=>app.publicStatus().canAnswer);
  const api=fakeApi({data:{installId:'second',settings:{port:mock.port,token:'lma_pl_DEMO_ONLY'}}});
  const second=new core.ExtensionApp(api);t.after(()=>second.client?.stop());await second.reload();await until(()=>second.status==='online');
  assert.equal((await second.answer('confirm',app.publicStatus().promptId)).ok,false);
  await app.answer('confirm',app.publicStatus().promptId);await until(()=>second.active);
  assert.equal((await second.uiMessage({kind:'manual-stop'})).ok,true);await until(()=>!mock.metrics().active);
});
test('real WebSocket: token rejection persists and prevents reconnects',async t=>{
  const mock=await createMockServer();const api=fakeApi({data:{settings:{port:mock.port,token:'lma_pl_WRONG'}}});const app=new core.ExtensionApp(api);
  t.after(async()=>{app.client?.stop();await mock.close();});await app.reload();await until(()=>app.status==='forbidden');
  assert.equal(api.store.authBlocked,true);await app.alarm();assert.equal(app.client.socket,null);
});
test('real WebSocket: startup page ignored and new page prompts after three seconds',async t=>{
  const mock=await createMockServer(),url=`http://127.0.0.1:${mock.port}/demo`;
  const api=fakeApi({history:false,detectionDelaySeconds:3,data:{settings:{port:mock.port,token:'lma_pl_DEMO_ONLY'}},tabs:[{id:1,url}]});const app=new core.ExtensionApp(api);
  t.after(async()=>{app.timers.clearTimeout(app.detectionTimer);app.client?.stop();await mock.close();});await app.reload();await until(()=>app.status==='online');
  assert.equal(app.ignoredCount,1);const at=Date.now();api.tabs.push({id:2,url});await app.tabEvent({kind:'created',id:2,url,at});
  assert.equal(app.publicStatus().canAnswer,false);await until(()=>app.publicStatus().canAnswer,4500);assert(Date.now()-at>=2900);
});
test('real WebSocket: replay of an old stop command cannot stop the next meeting',async t=>{
  const {mock,app}=await fixture(t,{mode:'auto'});await until(()=>app.active);
  const command={id:'stable-stop-id'};
  app.client.sendTabs(['https://localmeetassist.local/stop_manual'],command);
  await until(()=>mock.metrics().stops===1&&app.publicStatus().recordingActive===false);
  mock.manualStart();await until(()=>app.active);
  app.client.sendTabs(['https://localmeetassist.local/stop_manual'],command);
  await until(()=>app.client.pending.size===0);
  assert.equal(mock.metrics().stops,1);assert.equal(mock.metrics().active,true);
});
test('published server timing: rejects invalid N and grants N*poll after a silent socket is closed',async t=>{
  for(const missedPolls of [1,11,2.5])await assert.rejects(createMockServer({missedPolls}));
  for(const missedPolls of [2,4,10]){
    let clock=Date.now();const {mock,app}=await fixture(t,{mode:'auto',pollSeconds:10,missedPolls,now:()=>clock});
    await until(()=>app.active);await until(()=>app.client.pending.size===0);
    clock+=20001;mock.poll();await until(()=>mock.metrics().lost===1);app.client.stop();
    clock+=missedPolls*10000-1;mock.poll();assert.equal(mock.metrics().active,true);
    clock+=1;mock.poll();assert.equal(mock.metrics().active,false);assert.equal(mock.metrics().stops,1);
  }
});
