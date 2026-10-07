/** Development-only protocol peer. It never records audio; do not embed it in LocalMeetAssist. */
import http from 'node:http';
import { createHash } from 'node:crypto';
import { pathToFileURL } from 'node:url';

function frame(opcode, bytes) {
  bytes = Buffer.isBuffer(bytes) ? bytes : Buffer.from(bytes);
  const header = Buffer.alloc(bytes.length < 126 ? 2 : 4);
  header[0] = 0x80 | opcode;
  if (bytes.length < 126) header[1] = bytes.length;
  else { header[1] = 126; header.writeUInt16BE(bytes.length, 2); }
  return Buffer.concat([header, bytes]);
}
function peer(socket, onMessage, onClose) {
  let buffer = Buffer.alloc(0), closed = false;
  const connection = {
    send: message => { if (!closed) socket.write(frame(1, JSON.stringify(message))); },
    close: (code = 1000) => { if (!closed) { const b = Buffer.alloc(2); b.writeUInt16BE(code); socket.end(frame(8, b)); } },
    destroy: () => socket.destroy()
  };
  socket.on('data', bytes => {
    buffer = Buffer.concat([buffer, bytes]);
    while (buffer.length >= 2) {
      const opcode = buffer[0] & 15;
      if (!(buffer[0] & 128) || !(buffer[1] & 128) || buffer[0] & 112) { connection.close(1002); return; }
      let length = buffer[1] & 127, offset = 2;
      if (length === 127) { connection.close(1009); return; }
      if (length === 126) { if (buffer.length < 4) return; length = buffer.readUInt16BE(2); offset = 4; }
      if (buffer.length < offset + 4 + length) return;
      const mask = buffer.subarray(offset, offset + 4);
      const payload = Buffer.from(buffer.subarray(offset + 4, offset + 4 + length));
      for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i % 4];
      buffer = buffer.subarray(offset + 4 + length);
      if (opcode === 8) { connection.close(); return; }
      if (opcode === 9) { socket.write(frame(10, payload)); continue; }
      if (opcode === 10) continue;
      if (opcode !== 1) { connection.close(1003); return; }
      try { onMessage(JSON.parse(payload.toString('utf8')), connection); }
      catch { connection.send({ type: 'error', error: 'protocol_error' }); connection.close(1002); }
    }
  });
  socket.on('error', () => {});
  socket.on('close', () => { closed = true; onClose(connection); });
  return connection;
}

export async function createMockServer({ port=0, token='lma_pl_DEMO_ONLY', mode='notify', pollSeconds=20, missedPolls=4,
  now=()=>Date.now(), graceMilliseconds=missedPolls*pollSeconds*1000, journal={revision:0,active:false} }={}) {
  if(!Number.isInteger(missedPolls)||missedPolls<2||missedPolls>10)throw new Error('missedPolls must be 2–10');
  const peers=new Set(),clients=new Map(),nonempty=new Map(),commands=new Map();
  const lost=new Map(),graceTimers=new Map();let shuttingDown=false;
  let effectivePort=port,enabled=true,owner=null,promptId=null,nextPrompt=0,active=false,revision=journal.revision+(journal.active?1:0),starts=0,stops=0;
  Object.assign(journal,{revision,active:false}); // Test journal is in memory, not production durable storage.
  const state=()=>owner?(active?'recording':promptId?'awaiting_confirmation':'idle'):'idle';
  const recording=()=>({recording_active:active,recording_revision:revision});
  const config=()=>({type:'config',protocol_version:1,capabilities:['manual_tabs','recording_state'],enabled,mode,
    patterns:[`http://127.0.0.1:${effectivePort}/demo*`,'*meet.google.com/*'],poll_interval_seconds:pollSeconds,
    title_template:'{{title}} — {{date}} {{time}}',listen_port:effectivePort,effective_port:effectivePort});
  const sendState=client=>client.send({type:'state',state:promptId&&client.clientId!==owner?'idle':state(),owner:client.clientId===owner,...recording(),...(promptId&&client.clientId===owner?{prompt_id:promptId}:{})});
  const broadcast=()=>{for(const client of peers)if(client.clientId)sendState(client);};
  const start=clientId=>{
    if(active)return false;
    active=true;revision++;starts++;owner=clientId;promptId=null;Object.assign(journal,{revision,active});broadcast();return true;
  };
  const end=()=>{
    if(!active)return false;
    active=false;revision++;stops++;owner=null;promptId=null;Object.assign(journal,{revision,active});broadcast();return true;
  };
  const forgetLoss=id=>{lost.delete(id);clearTimeout(graceTimers.get(id));graceTimers.delete(id);};
  const expireLosses=()=>{for(const [id,deadline]of lost)if(now()>=deadline){
    forgetLoss(id);
    if(owner===id&&!clients.has(id)){
      if(active)end();else{owner=null;promptId=null;broadcast();}
    }
  }};
  function tabs(message,client){
    if(!Number.isSafeInteger(message.seq)||message.seq<=(client.seq||0)||!Array.isArray(message.urls)||message.urls.some(url=>typeof url!=='string'))throw new Error('protocol');
    client.seq=message.seq;
    const markers=message.urls.filter(url=>['https://localmeetassist.local/record_manual','https://localmeetassist.local/stop_manual'].includes(url));
    const before=nonempty.get(client.clientId)||false;
    const after=message.urls.some(url=>url.startsWith(`http://127.0.0.1:${effectivePort}/demo`)||/^https?:\/\/meet\.google\.com\//i.test(url));
    nonempty.set(client.clientId,after);
    if(markers.length){
      let result='rejected';
      if(markers.length===1&&typeof message.command_id==='string'&&message.command_id&&message.command_id.length<=256){
        const key=client.clientId+':'+message.command_id,old=commands.get(key);
        if(old)result=old.marker===markers[0]?old.result:'rejected';
        else{
          if(enabled)result=(markers[0].endsWith('/record_manual')?start(null):end())?'applied':'noop';
          commands.set(key,{marker:markers[0],result});
        }
      }
      sendState(client);
      client.send({type:'ack',seq:message.seq,command_id:message.command_id||'invalid',command_result:result});return;
    }
    if(enabled&&!active){
      if(!after&&client.clientId===owner){promptId=null;owner=null;broadcast();}
      else if(after&&!before&&!promptId){
        if(mode==='auto')start(client.clientId);
        else{owner=client.clientId;promptId='mock-'+(++nextPrompt);broadcast();}
      }
    }
    // Empty tabs never stop recording; a missing browser owner is handled by the separate grace window.
    client.send({type:'ack',seq:message.seq});
  }
  const server=http.createServer((request,response)=>{
    response.writeHead(200,{'Content-Type':'text/html; charset=utf-8'});
    response.end('<!doctype html><html lang="ru"><meta charset="utf-8"><title>LocalMeetAssist — тестовый сервер</title><body><h1>Тестовый сервер</h1><p>Аудио не записывается.</p><a href="/demo">Открыть страницу тестовой встречи</a></body></html>');
  });
  server.on('upgrade',(request,socket,head)=>{
    const origin=request.headers.origin;
    if(request.url!=='/api/v1/ws'||(origin&&!/^(chrome|moz)-extension:\/\//.test(origin))){socket.end('HTTP/1.1 403 Forbidden\r\n\r\n');return;}
    const accept=createHash('sha1').update(request.headers['sec-websocket-key']+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64');
    socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: '+accept+'\r\n\r\n');
    let client;
    client=peer(socket,(message,c)=>{
      c.lastSeen=now();
      if(!c.clientId){
        if(message.type!=='hello'||message.token!==token||typeof message.client_id!=='string'){c.send({type:'error',error:'forbidden'});c.close(1008);return;}
        if(message.protocol_version!==1){c.send({type:'error',error:'unsupported_protocol'});c.close(1002);return;}
        expireLosses();
        const old=clients.get(message.client_id);c.clientId=message.client_id;clients.set(c.clientId,c);forgetLoss(c.clientId);old?.close();
        c.send(config());sendState(c);return;
      }
      if(message.type==='tabs')tabs(message,c);
      else if(message.type==='confirm'||message.type==='decline'){
        if(c.clientId===owner&&message.prompt_id===promptId&&promptId){
          if(message.type==='confirm'){if(!start(c.clientId)){owner=null;promptId=null;broadcast();}}
          else{promptId=null;owner=null;broadcast();}
        }
      }else throw new Error('protocol');
    },c=>{
      peers.delete(c);
      if(clients.get(c.clientId)!==c)return; // Closing a replaced socket must not detach the restored owner.
      clients.delete(c.clientId);
      if(!shuttingDown&&owner===c.clientId){
        lost.set(c.clientId,now()+graceMilliseconds);
        graceTimers.set(c.clientId,setTimeout(expireLosses,graceMilliseconds));
      }
    });
    peers.add(client);client.lastSeen=now();if(head.length)socket.emit('data',head);
  });
  await new Promise(resolve=>server.listen(port,'127.0.0.1',resolve));effectivePort=server.address().port;
  const poll=()=>{expireLosses();for(const c of peers)if(c.clientId){
    if(now()-c.lastSeen>pollSeconds*2000)c.close();else c.send({type:'ask',what:'tabs',owner:c.clientId===owner,...recording()});
  }};
  const interval=setInterval(poll,pollSeconds*1000);
  return {port:effectivePort,metrics:()=>({state:state(),active,revision,starts,stops,owner,commands:commands.size,lost:lost.size}),
    manualStart:()=>start(null),manualEnd:end,manualStopBrowser:end,poll,
    setEnabled:value=>{enabled=value;if(!value){owner=null;promptId=null;nonempty.clear();for(const id of lost.keys())forgetLoss(id);}
      for(const c of peers)if(c.clientId)c.send(config());broadcast();},
    close:async()=>{shuttingDown=true;clearInterval(interval);for(const id of lost.keys())forgetLoss(id);for(const c of peers)c.destroy();await new Promise(resolve=>server.close(resolve));}};
}

if(import.meta.url===pathToFileURL(process.argv[1]||'').href){
  const server=await createMockServer({port:Number(process.env.LMA_MOCK_PORT||52469),mode:process.env.LMA_MOCK_MODE||'notify'});
  console.log(`Development mock: http://127.0.0.1:${server.port}/demo · token lma_pl_DEMO_ONLY · no audio recording`);
}
