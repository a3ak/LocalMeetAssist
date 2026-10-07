import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFile} from 'node:fs/promises';
function node(){return{value:'',dataset:{},style:{},hidden:false,children:[],handlers:{},attributes:{},addEventListener(k,fn){this.handlers[k]=fn;},setAttribute(k,v){this.attributes[k]=v;},append(...nodes){this.children.push(...nodes);},replaceChildren(...nodes){this.children=nodes;}};}
test('real popup scripts render source/list, send controls and never render URL as HTML',async()=>{
  const html=await readFile(new URL('../src/popup.html',import.meta.url),'utf8');
  const nodes=Object.fromEntries([...html.matchAll(/\bid="([^"]+)"/g)].map(m=>[m[1],node()]));
  const malicious='https://meet.google.com/<img onerror=alert(1)>';
  const row={id:1,url:malicious,glob:'*meet.google.com/*',source:true,controlling:true};
  const status={ok:true,status:'online',version:'0.1.0',port:52469,recordingActive:true,canControl:true,canAnswer:false,openCount:1,rows:[row],origin:{id:1,url:malicious,glob:row.glob},controller:{id:1,url:malicious},lastHeartbeat:Date.now()};
  const calls=[];let interval;
  const sandbox={document:{getElementById:id=>{assert(nodes[id],'Missing '+id);return nodes[id];},createElement:()=>node()},
    LMA:{browserApi:{openOptions(){}},ui:{showStatus(){},async request(message){calls.push(message);return message.kind==='status'?structuredClone(status):message.kind==='active-tab'?{ok:true,tab:{id:88,url:malicious}}:{ok:true};}}},
    setInterval:fn=>(interval=fn,1),clearInterval(){},addEventListener(){}};
  vm.createContext(sandbox);vm.runInContext(await readFile(new URL('../src/popup.js',import.meta.url),'utf8'),sandbox);
  for(let i=0;i<8;i++)await new Promise(resolve=>setImmediate(resolve));
  assert.equal(nodes['source-url'].textContent,malicious);assert.equal(nodes['record-circle'].hidden,true);assert.equal(nodes['record-square'].hidden,false);
  nodes.matches.handlers.click();assert.equal(nodes['url-view'].hidden,false);
  const group=nodes['url-list'].children[0],entry=group.children[2];assert.equal(entry.children[0].children[0].textContent,malicious);
  await entry.children[1].handlers.click();assert(calls.some(m=>m.kind==='close-tab'&&m.tabId===1));
  await nodes['close-all'].handlers.click();assert(calls.some(m=>m.kind==='close-all'));
  await nodes['record-control'].handlers.click();assert(calls.some(m=>m.kind==='manual-stop'));
  await nodes['open-app'].handlers.click();assert(calls.some(m=>m.kind==='open-app'));
  status.restartWaiting=true;status.switchAt=Date.now()+60000;status.controller=null;status.notice='Браузер перезапущен';await interval();
  assert.match(nodes['recording-notice'].textContent,/Браузер перезапущен.*осталось 60 с/);
  assert.equal(nodes['select-controller-panel'].hidden,false);assert.equal(nodes['controller'].hidden,true);
  await nodes['select-controller'].handlers.click();assert(calls.some(m=>m.kind==='select-controller'));
  status.restartWaiting=false;status.switchAt=null;
  status.recordingActive=false;status.origin=null;await interval();
  assert.equal(nodes['record-circle'].hidden,false);await nodes['record-control'].handlers.click();assert(calls.some(m=>m.kind==='manual-start'));
  status.canControl=false;status.recordingActive=null;status.status='offline';await interval();assert.equal(nodes['record-control'].disabled,true);
  nodes.back.handlers.click();assert.equal(nodes['main-view'].hidden,false);
});
test('shared UI explains unpinned ports, pending application restart and unsupported protocol',async()=>{
  const html=await readFile(new URL('../src/popup.html',import.meta.url),'utf8');
  const nodes=Object.fromEntries([...html.matchAll(/\bid="([^"]+)"/g)].map(m=>[m[1],node()]));
  const sandbox={LMA:{},document:{getElementById:id=>nodes[id]||null}};vm.createContext(sandbox);
  vm.runInContext(await readFile(new URL('../src/ui.js',import.meta.url),'utf8'),sandbox);
  const base={status:'online',recordingActive:false,compatible:true,listenPort:0,effectivePort:52469};
  sandbox.LMA.ui.showStatus(base);assert.equal(nodes['port-warning'].hidden,false);assert.match(nodes['port-warning'].textContent,/не закреплён/);
  sandbox.LMA.ui.showStatus({...base,listenPort:52500});assert.match(nodes['port-warning'].textContent,/после перезапуска/);
  sandbox.LMA.ui.showStatus({...base,listenPort:52469});assert.equal(nodes['port-warning'].hidden,true);
  sandbox.LMA.ui.showStatus({...base,status:'unsupported_protocol'});assert.match(nodes['connection-detail'].textContent,/отклонило протокол 1/);
});
