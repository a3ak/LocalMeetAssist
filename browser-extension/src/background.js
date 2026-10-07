(() => {
  const ns = globalThis.LMA;
  ns.ExtensionApp = class {
    constructor(api, { socketFactory, timers, uuid = () => crypto.randomUUID(), now = () => Date.now() } = {}) {
      this.api = api; this.dependencies = { socketFactory, timers, now }; this.uuid = uuid; this.now = now;
      this.timers = timers || globalThis; this.store = new ns.SettingsStore(api, uuid);
      this.notifications = new ns.NotificationManager(api);
      this.tracker = new ns.TabTracker(api, now); this.control = new ns.MeetingController(this.tracker, now);
      this.state = { state: 'idle', owner: false }; this.expressions = []; this.status = 'not_configured'; this.epoch = 0;
      this.scans = Promise.resolve(); this.reloadQueue = Promise.resolve(); this.actionQueue = Promise.resolve();
      this.matchedCount = this.pendingCount = this.ignoredCount = 0;
    }
    reload() { const task = this.reloadQueue.then(() => this.load()); this.reloadQueue = task.catch(() => {}); return task; }
    async settingsChanged() {
      const loaded = await this.store.load();
      if (this.settings && loaded.settings && this.client && !this.client.blocked && !loaded.authBlocked && !loaded.protocolBlocked
        && loaded.settings.port === this.settings.port && loaded.settings.token === this.settings.token) {
        this.settings = loaded.settings; await this.snapshot(true); await this.updateBadge(); return;
      }
      return this.reload();
    }
    async load() {
      await (this.initialized ||= (async () => {
        await this.tracker.init(); await this.control.init();
        const saved = (await this.api.sessionGet('pendingCommand')).pendingCommand;
        if (saved && ['start','stop'].includes(saved.kind) && typeof saved.id === 'string' && Number.isFinite(saved.at)) this.command = saved;
      })());
      const epoch = ++this.epoch;
      this.timers.clearTimeout(this.detectionTimer); this.detectionDeadline = null; this.detectionTimer = null;
      await this.api.scanAlarm(null);
      this.client?.stop(); this.client = null; this.config = null; this.state = { state: 'idle', owner: false };
      this.expressions = []; this.sentSignature = null; this.recordingKnown = false;
      this.control.rows = []; this.openCount = this.matchedCount = this.pendingCount = this.ignoredCount = 0;
      const loaded = await this.store.load();
      if (this.settings && (!loaded.settings || this.settings.port !== loaded.settings.port || this.settings.token !== loaded.settings.token)) {
        await this.control.reset(); await this.cancelCommand();
      }
      this.settings = loaded.settings; this.clientId = loaded.clientId; this.control.clientId = this.clientId;
      if (!this.settings || loaded.authBlocked || loaded.protocolBlocked) {
        this.status = loaded.authBlocked ? 'forbidden' : loaded.protocolBlocked ? 'unsupported_protocol' : 'not_configured';
        await this.api.clearAlarm(); await this.notifications.sync(this.state,{enabled:false}); await this.updateBadge(); return;
      }
      await this.api.ensureAlarm(0.5);
      this.client = new ns.SocketClient({ ...this.dependencies, settings:this.settings, clientId:this.clientId,
        onMessage: message => this.receive(message,epoch),
        onStatus: async status => {
          if (epoch !== this.epoch) return;
          this.status = status;
          if (status === 'connecting') { this.sentSignature = null; this.commandSentGeneration = null; this.recordingKnown = false; }
          if (!['online','connecting'].includes(status)) this.recordingKnown = false;
          await this.updateBadge();
        },
        onForbidden: async () => {
          if (epoch !== this.epoch) return;
          await this.api.storageSet({authBlocked:true}); await this.api.clearAlarm();
          this.timers.clearTimeout(this.detectionTimer); await this.api.scanAlarm(null);
          await this.notifications.sync({state:'idle',owner:false},{enabled:false});
        },
        onUnsupported: async () => {
          if (epoch !== this.epoch) return;
          await this.api.storageSet({protocolBlocked:true,protocolBlockedVersion:1}); await this.api.clearAlarm();
          this.timers.clearTimeout(this.detectionTimer); await this.api.scanAlarm(null);
          await this.notifications.sync({state:'idle',owner:false},{enabled:false});
        }
      });
      this.client.connect();
    }
    get compatible() { return !!(this.config?.protocol_version === 1 && this.config.capabilities?.includes('manual_tabs') && this.config.capabilities.includes('recording_state')); }
    get active() { return this.recordingKnown && this.control.data.active === true; }
    async updateBadge() {
      const waiting = this.status === 'online' && this.config?.enabled && this.matchedCount > 0 && this.state.owner && this.state.state === 'awaiting_confirmation';
      const disconnected = this.status !== 'online';
      const presentation = { text:this.status==='not_configured'?'':disconnected?'!':waiting?'?':'', recording:!disconnected&&this.active,
        color:disconnected?'#b45309':waiting?'#facc15':'#4263eb',textColor:waiting?'#111827':'#ffffff',
        title:'LocalMeetAssist — '+(disconnected?'Состояние записи неизвестно':this.active?(this.control.data.notice||'Идёт запись встречи'):waiting?'Начать запись встречи? Нажмите значок, чтобы ответить':this.config?.enabled===false?'Интеграция выключена':this.config?.mode==='notify'?'Спрашивать перед записью — ожидание встречи':'Автоматическая запись — ожидание встречи') };
      const task = this.actionQueue.then(()=>this.api.presentAction(presentation)); this.actionQueue=task.catch(()=>{}); await task;
    }
    async recording(message) {
      if (typeof message.recording_active !== 'boolean' || !Number.isSafeInteger(message.recording_revision)) {
        if (this.compatible) throw new Error('protocol');
        this.recordingKnown = false; return;
      }
      const task = this.scans.then(async()=>{
        if (this.control.data.revision !== null && message.recording_revision < this.control.data.revision) throw new Error('revision regressed');
        this.control.recording(message.recording_active,message.recording_revision,message.owner,
          message.type==='state'?message.state:this.state.state);
        if (this.command?.automatic && (!message.recording_active || !this.control.data.owned || message.recording_revision!==this.command.revision)) await this.cancelCommand();
        this.recordingKnown=true; this.lastHeartbeat=this.now(); await this.control.persist();
      });
      this.scans=task.catch(()=>{}); await task;
    }
    async receive(message,epoch) {
      if (epoch!==this.epoch) return;
      switch(message.type) {
        case 'config':
          this.config=message; this.expressions=ns.compilePatterns(message.patterns);
          if (!message.enabled) {
            const task=this.scans.then(async()=>{this.control.clearControl();this.control.data.owned=false;
              this.control.recoveryPending=false;this.control.recovery=null;await this.cancelCommand();await this.control.persist();});
            this.scans=task.catch(()=>{});await task;
          }
          if (this.compatible) await this.snapshot(true,epoch); else await this.notifications.sync({state:'idle',owner:false},{enabled:false});
          break;
        case 'state':
          this.state={state:message.state,owner:message.owner,prompt_id:message.prompt_id};
          await this.recording(message); await this.snapshot(true,epoch);
          await this.notifications.sync(this.matchedCount>0&&this.compatible?this.state:{state:'idle',owner:false},this.config);
          break;
        case 'ask': await this.recording(message); await this.snapshot(true,epoch); break;
        case 'ack':
          if (this.command && message.command_id===this.command.id && ['applied','noop','rejected'].includes(message.command_result)) {
            this.commandError=message.command_result==='rejected'?'Приложение отклонило действие. Проверьте запись в приложении.':null;
            this.command=null; this.commandSentGeneration=null; await this.api.sessionSet({pendingCommand:null});
          }
          await this.snapshot(false,epoch); break;
      }
      await this.updateBadge();
    }
    snapshot(force=false,epoch=this.epoch) {
      const task=this.scans.then(async()=>{
        if(epoch!==this.epoch||!this.config||!this.client?.hasConfig||!this.compatible)return;
        const client=this.client,generation=client.generation;
        const tabs=await this.api.queryTabs(); if(epoch!==this.epoch||generation!==client.generation)return;
        if (this.control.data.needsEmpty) {
          this.tracker.collect(tabs,this.expressions,this.settings,this.config.enabled);
          this.tracker.invalidate(); delete this.control.data.candidate;
          if (this.config.enabled && client.hasState && this.recordingKnown && client.sendTabs([])!==null) {
            this.sentSignature='[]'; delete this.control.data.needsEmpty;
          }
        }
        const result=this.control.collect(tabs,this.config,this.expressions,this.settings);
        this.pendingCount=result.pendingCount;this.ignoredCount=result.ignoredCount;this.matchedCount=result.urls.length;
        this.openCount=result.rows.length;
        if(result.stopDue&&this.recordingKnown&&client.hasState&&!this.command&&this.config.enabled&&!this.control.data.stopRequested){
          this.control.data.stopRequested=true;await this.queueCommand('stop',true);
        }
        await this.control.persist(); await this.scheduleDetection(result.nextDeadline);
        const urls=[...result.urls]; let command=null;
        if(this.command&&this.config.enabled&&this.client.hasState&&this.commandSentGeneration!==generation){
          if(this.now()-this.command.at>120000){this.command=null;await this.api.sessionSet({pendingCommand:null});this.commandError='Действие устарело. Повторите его.';}
          else {command=this.command;urls.push(ns.CONTROL_URLS[command.kind]);}
        }
        const signature=JSON.stringify(urls);
        if(this.config.enabled&&client.hasState&&this.recordingKnown&&(force||signature!==this.sentSignature||command)){
          if(client.sendTabs(urls,command)!==null){this.sentSignature=signature;if(command)this.commandSentGeneration=generation;}
        }
      });
      this.scans=task.catch(()=>{});return task;
    }
    async cancelCommand() { this.command=null;this.commandSentGeneration=null;await this.api.sessionSet({pendingCommand:null}); }
    async queueCommand(kind,automatic=false) {
      if(this.command)return false;
      this.command={kind,id:this.uuid(),at:this.now(),...(automatic?{automatic:true,revision:this.control.data.revision}:{})}; this.commandSentGeneration=null;this.commandError=null;
      await this.api.sessionSet({pendingCommand:{...this.command}});return true;
    }
    async scheduleDetection(deadline) {
      if(deadline===this.detectionDeadline&&(deadline===null||this.detectionTimer))return;
      this.timers.clearTimeout(this.detectionTimer);this.detectionDeadline=deadline;this.detectionTimer=null;
      await this.api.scanAlarm(deadline);
      if(deadline!==null)this.detectionTimer=this.timers.setTimeout(()=>{
        this.detectionTimer=null;this.tabEvent().catch(()=>{});
      },Math.max(0,Math.min(20000,deadline-this.now())));
    }
    async tabEvent(event) {
      await this.initialized;
      const task=this.scans.then(async()=>{this.control.observe(event);this.tracker.observe(event,this.expressions);await this.control.persist();});
      this.scans=task.catch(()=>{});await task;
      if(!this.client||this.client.blocked)return;
      if(!this.client.socket)this.client.connect();else await this.snapshot();
      await this.updateBadge();
    }
    async alarm(){if(!this.client||this.client.blocked)return;if(this.client.open&&this.client.hasConfig)await this.snapshot();else this.client.connect();}
    publicStatus(){
      const d=this.control.data;
      return {status:this.status,state:this.state.state,owner:this.state.owner,
        promptId:this.matchedCount>0&&this.state.owner&&this.state.state==='awaiting_confirmation'?this.state.prompt_id:null,
        port:this.settings?.port||null,clientId:this.clientId,enabled:this.config?.enabled??null,mode:this.config?.mode||null,
        listenPort:this.config?.listen_port??null,effectivePort:this.config?.effective_port??null,
        matchedCount:this.matchedCount,openCount:this.openCount||0,pendingCount:this.pendingCount,ignoredCount:this.ignoredCount,
        notificationStatus:this.notifications.status,version:this.api.version,compatible:this.compatible,
        recordingActive:this.recordingKnown?d.active:null,lastHeartbeat:this.lastHeartbeat||null,
        canAnswer:this.status==='online'&&this.compatible&&this.matchedCount>0&&this.config?.enabled&&this.state.owner&&this.state.state==='awaiting_confirmation',
        canControl:this.status==='online'&&this.compatible&&this.recordingKnown&&this.config?.enabled&&!this.command,
        commandPending:!!this.command,commandError:this.commandError||null,
        origin:d.origin||null,controller:d.controller||null,switchAt:d.switchAt||null,stopAt:d.stopAt||null,notice:d.notice||null,restartWaiting:!!d.restartWaiting,
        rows:this.control.rows.map(row=>({id:row.id,url:row.url,glob:row.glob,title:row.title,
          source:d.active&&row.id===d.origin?.id&&row.url===d.origin.url,
          controlling:d.active&&row.id===d.controller?.id,
          reason:d.active&&row.id===d.controller?.id?'':row.enteredAt===null?(row.reason||'Устаревшая'):row.admitted?'':'Ожидает задержку'}))};
    }
    async answer(kind,promptId){
      if(!['confirm','decline'].includes(kind)||typeof promptId!=='string')return{ok:false,error:'Подтверждение устарело.'};
      const client=this.client;
      if(!client||client.blocked)return{ok:false,error:'Нет соединения с LocalMeetAssist.'};
      try{await client.ensureReady();}catch{return{ok:false,error:'Нет соединения. Проверьте, запущено ли приложение.'};}
      await this.snapshot(true);
      if(client!==this.client||!this.publicStatus().canAnswer||this.state.prompt_id!==promptId)return{ok:false,error:'Подтверждение больше не актуально.'};
      if(!client.send({type:kind,prompt_id:promptId}))return{ok:false,error:'Соединение прервано.'};
      if(kind==='decline'){
        const task=this.scans.then(async()=>{this.control.decline();await this.control.persist();});this.scans=task.catch(()=>{});await task;
        await this.snapshot(true);
      }
      return{ok:true};
    }
    async uiMessage(message){
      if(!message||typeof message.kind!=='string')return{ok:false};
      switch(message.kind){
        case 'status': await this.snapshot(); return{ok:true,...this.publicStatus()};
        case 'save-settings':try{return{ok:true,changed:await this.store.save(message.port,message.token,message.maxAgeSeconds,message.detectionDelaySeconds,message.stopDelaySeconds)};}catch(error){return{ok:false,error:error.message};}
        case 'clear-settings':await this.store.clear();return{ok:true};
        case 'test-notification':return this.notifications.test();
        case 'reconnect':if(!this.client||this.client.blocked)return{ok:false,error:'Укажите порт и действующий токен.'};this.client.connect();return{ok:true};
        case 'confirm':case 'decline':return this.answer(message.kind,message.promptId);
        case 'manual-start':case 'manual-stop':
          if(!this.publicStatus().canControl)return{ok:false,error:'Нет актуального состояния записи или действие уже отправлено.'};
          await this.queueCommand(message.kind==='manual-start'?'start':'stop');await this.snapshot(true);return{ok:true};
        case 'wait-switch':
          if(!this.publicStatus().canControl)return{ok:false,error:'Нет связи с приложением.'};
          try{this.control.startSwitch();await this.control.persist();await this.snapshot(true);return{ok:true};}catch(error){return{ok:false,error:error.message};}
        case 'select-controller':
          if(!this.publicStatus().canControl)return{ok:false,error:'Нет связи с приложением.'};
          try{this.control.select(await this.api.activeTab());await this.control.persist();await this.snapshot(true);return{ok:true};}catch(error){return{ok:false,error:error.message};}
        case 'open-app':
          if(!this.settings?.port)return{ok:false,error:'Укажите порт в настройках.'};
          await this.api.openTab(`http://127.0.0.1:${this.settings.port}/`);return{ok:true};
        case 'active-tab': { const tab=await this.api.activeTab();return{ok:true,tab:tab?{id:tab.id,url:tab.url||'',title:tab.title||''}:null}; }
        case 'close-tab':case 'close-all':{
          await this.snapshot(); const rows=this.publicStatus().rows;
          const allowed=rows.filter(row=>message.kind==='close-all'||row.id===message.tabId);
          const controller=this.control.data.controller;
          if(message.kind==='close-tab'&&controller?.linked&&controller.id===message.tabId&&!allowed.length)allowed.push(controller);
          if(!allowed.length)return{ok:false,error:'Подходящая вкладка уже закрыта.'};
          for(const row of allowed){
            const current=(await this.api.queryTabs()).find(tab=>tab.id===row.id);
            if(current?.url===row.url){await this.api.closeTabs([row.id]);await this.tabEvent({kind:'removed',id:row.id});}
          }
          return{ok:true};
        }
        default:return{ok:false};
      }
    }
  };
  if(!ns.browserApi)return;
  const api=ns.browserApi,app=new ns.ExtensionApp(api);let ready;
  const run=action=>Promise.resolve(ready).then(action).catch(()=>{});
  api.onAlarm(()=>run(()=>app.alarm()));api.onTabs(event=>run(()=>app.tabEvent(event)));
  api.onStartup(()=>run(()=>app.alarm()));api.onInstalled(()=>run(()=>app.alarm()));
  api.onStorage(changes=>{if(changes.settings)run(()=>app.settingsChanged());});
  api.onMessage(message=>Promise.resolve(ready).then(()=>app.uiMessage(message)));
  api.onNotificationClick(id=>{const prompt=ns.promptFromNotification(id);if(prompt)run(()=>app.answer('confirm',prompt));});
  api.onNotificationButton((id,button)=>{const prompt=ns.promptFromNotification(id);if(prompt&&(button===0||button===1))run(()=>app.answer(button===0?'confirm':'decline',prompt));});
  api.onNotificationClosed(()=>{});
  ready=api.storageRestrict().then(()=>app.reload());
})();
