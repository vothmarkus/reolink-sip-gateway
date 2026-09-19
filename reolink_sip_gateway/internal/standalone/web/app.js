'use strict';
const $ = id => document.getElementById(id);
let settings, revision, csrf, storedSecrets = {}, clearSecrets = new Set();
let dirty = false, authenticated = false, lastStatus = null, pollTimer;
const groups = {
  'reolink-fields': [
    ['reolink.reolink_host','Kamera- oder NVR-Adresse',{placeholder:'192.168.178.50'}],
    ['reolink.reolink_mode','Verbindung',{choices:[['auto','Automatisch'],['nvr','Über NVR'],['standalone','Kamera direkt']]}],
    ['reolink.reolink_username','Benutzername',{autocomplete:'username'}],
    ['reolink.reolink_password','Passwort',{secret:'reolink'}],
    ['reolink.nvr_channel_number','NVR-Kanal',{type:'number',min:1,max:256,hint:'Die Kanalnummer beginnt bei 1.'}],
    ['reolink.baichuan_port','Reolink-Dienstport',{type:'number',min:1,max:65535,hint:'Standard: 9000. Für den direkten Klingeltrigger erforderlich.'}],
    ['reolink.reolink_rtsp_port','RTSP-Port',{type:'number',min:1,max:65535}]
  ],
  'trigger-fields': [
    ['trigger.source','Auslöser',{choices:[['baichuan','Reolink-Klingeltaste direkt'],['manual','Nur Testanrufe / eingehende Anrufe']]}],
    ['trigger.route_id','Route bei Klingeldruck',{routes:true}]
  ],
  'sip-fields': [
    ['sip.sip_registrar','FRITZ!Box / SIP-Server',{placeholder:'auto',hint:'auto = Standardgateway; alternativ IP-Adresse oder Hostname.'}],
    ['sip.sip_registrar_port','SIP-Serverport',{type:'number',min:1,max:65535}],
    ['sip.door_call_enabled','Türsprechkonto verwenden',{type:'checkbox'}],
    ['sip.sip_display_name','Anzeigename',{}],
    ['sip.sip_username','SIP-Benutzername',{autocomplete:'username'}],
    ['sip.sip_password','SIP-Passwort',{secret:'sip'}],
    ['sip.sip_local_port','Lokaler SIP-Port',{type:'number',min:1,max:65535}],
    ['sip.sip_codec_preference','Sprachcodec',{choices:[['pcma','G.711 A-law'],['pcmu','G.711 µ-law'],['auto','Automatisch']]}]
  ],
  'parallel-fields': [
    ['sip.parallel_call_enabled','Mobilrufe aktivieren',{type:'checkbox'}],
    ['sip.parallel_local_port','Lokaler Port des Mobilrufkontos',{type:'number',min:1,max:65535}],
    ['sip.parallel_username','SIP-Benutzername',{autocomplete:'username'}],
    ['sip.parallel_password','SIP-Passwort',{secret:'parallel'}]
  ],
  'audio-fields': [
    ['audio.echo_cancellation_enabled','Echounterdrückung',{type:'checkbox'}],
    ['audio.webrtc_high_pass_filter_enabled','Hochpassfilter',{type:'checkbox'}],
    ['audio.webrtc_noise_suppression_enabled','Rauschunterdrückung',{type:'checkbox'}]
  ],
  'call-fields': [
    ['call.incoming_calls_enabled','Eingehende Anrufe automatisch annehmen',{type:'checkbox'}],
    ['call.incoming_connection_tone_enabled','Verbindungston vor Annahme',{type:'checkbox'}],
    ['call.incoming_allowed_callers','Erlaubte Anrufer',{list:true,hint:'Kommagetrennte Rufnummern; * erlaubt alle Anrufer.'}],
    ['call.debounce_seconds','Abstand zwischen Klingelrufen (s)',{type:'number',min:0,max:60}],
    ['call.ring_timeout_seconds','Klingeldauer (s)',{type:'number',min:5,max:180}],
    ['call.rtp_inactivity_timeout_seconds','Zeitgrenze ohne Audiodaten (s)',{type:'number',min:5,max:120}],
    ['call.max_call_duration_seconds','Maximale Gesprächsdauer (s)',{type:'number',min:15,max:3600}]
  ],
  'operation-fields': [
    ['diagnostics.dry_run','Passivmodus',{type:'checkbox'}],
    ['live_image.fritzfon_live_image_enabled','FRITZ!Fon-Livebilder',{type:'checkbox'}],
    ['diagnostics.log_level','Protokollumfang',{choices:[['info','Normal'],['debug','Ausführlich'],['warn','Warnungen'],['error','Nur Fehler']]}]
  ]
};

function get(path) { return path.split('.').reduce((value,key)=>value?.[key],settings); }
function set(path,value) { const parts=path.split('.');const key=parts.pop();parts.reduce((value,key)=>value[key],settings)[key]=value; }
function markDirty() { dirty=true;$('save-state').textContent='Änderungen noch nicht übernommen'; }
function notice(message,error=false,login=false) { const el=$(login?'login-notice':'notice');el.textContent=message;el.classList.toggle('error',error);el.hidden=!message; }

async function request(path,options={}) {
  const response=await fetch(path,{credentials:'same-origin',...options});
  const data=await response.json();
  if(!response.ok) { const error=new Error(data.error?.message||'Anfrage fehlgeschlagen.');error.status=response.status;throw error; }
  return data;
}
function post(path,data) { return request(path,{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf||''},body:JSON.stringify(data)}); }

function field(path,label,options={}) {
  const box=document.createElement('div');box.className='field';
  const title=document.createElement('label');const id='field-'+path.replaceAll('.','-');title.htmlFor=id;title.textContent=label;
  let input;
  if(options.choices||options.routes) {
    input=document.createElement('select');
    if(options.routes) input.dataset.routeSelect='true';
    for(const [value,text] of options.choices||settings.call_routes.map(route=>[route.id,route.name||route.id])) { input.add(new Option(text,value)); }
  } else { input=document.createElement('input');input.type=options.secret?'password':options.type||'text'; }
  input.id=id;
  if(options.type==='checkbox') { box.classList.add('check');input.checked=Boolean(get(path));title.prepend(input);box.append(title); }
  else { input.value=options.list?(get(path)||[]).join(', '):(get(path)??'');box.append(title,input); }
  if(options.min!==undefined) input.min=options.min;
  if(options.max!==undefined) input.max=options.max;
  if(options.type==='number') input.step='1';
  if(options.autocomplete) input.autocomplete=options.autocomplete;
  if(options.placeholder) input.placeholder=options.placeholder;
  if(options.secret) {
    input.autocomplete='new-password';input.value=get(path)||'';
    input.placeholder=storedSecrets[options.secret]?'Gespeichert – leer lassen zum Beibehalten':'Passwort eingeben';
    if(storedSecrets[options.secret]) {
      const clear=document.createElement('label');clear.className='secret-clear';
      const check=document.createElement('input');check.type='checkbox';check.checked=clearSecrets.has(options.secret);
      check.addEventListener('change',()=>{check.checked?clearSecrets.add(options.secret):clearSecrets.delete(options.secret);markDirty();});
      clear.append(check,document.createTextNode('Gespeichertes Passwort löschen'));box.append(clear);
    }
  }
  if(options.hint) { const hint=document.createElement('small');hint.textContent=options.hint;box.append(hint); }
  input.addEventListener('input',()=>{
    const value=options.type==='checkbox'?input.checked:options.type==='number'?Number(input.value):options.list?input.value.split(',').map(v=>v.trim()).filter(Boolean):input.value;
    set(path,value);markDirty();
    if(path.startsWith('call_routes.')) refreshRouteOptions();
  });
  return box;
}

function refreshRouteOptions() {
  for(const select of document.querySelectorAll('[data-route-select]')) {
    select.replaceChildren();for(const route of settings.call_routes)select.add(new Option(route.name||route.id,route.id));
    select.value=settings.trigger.route_id;
  }
  const test=$('test-route'),previous=test.value;test.replaceChildren();
  for(const route of settings.call_routes)test.add(new Option(route.name||route.id,route.id));
  if(settings.call_routes.some(route=>route.id===previous))test.value=previous;
  updateCallControls();
}

function renderRoutes() {
  const holder=$('routes');holder.replaceChildren();
  settings.call_routes.forEach((route,index)=>{
    const card=document.createElement('article');card.className='route';
    const heading=document.createElement('div');heading.className='route-head';const title=document.createElement('strong');title.textContent='Route '+(index+1);
    const remove=document.createElement('button');remove.type='button';remove.className='quiet';remove.textContent='Entfernen';remove.disabled=settings.call_routes.length===1;
    remove.addEventListener('click',()=>{settings.call_routes.splice(index,1);if(settings.trigger.route_id===route.id)settings.trigger.route_id=settings.call_routes[0].id;markDirty();renderRoutes();refreshRouteOptions();});
    heading.append(title,remove);card.append(heading);
    const fields=document.createElement('div');fields.className='fields';
    for(const [key,label,hint] of [['id','Kennung','Kleinbuchstaben, Zahlen und Unterstriche.'],['name','Name',''],['doorbell_number','FRITZ!Box-Klingeltaste','Leer lassen, wenn nur Mobilruf gewünscht ist.'],['mobile_number_1','Mobilrufnummer 1',''],['mobile_number_2','Mobilrufnummer 2',''],['mobile_number_3','Mobilrufnummer 3','']]) {
      fields.append(field('call_routes.'+index+'.'+key,label,{hint}));
    }
    card.append(fields);holder.append(card);
  });
  $('add-route').disabled=settings.call_routes.length>=32;
}

function render() {
  for(const [id,descriptors] of Object.entries(groups)) { const target=$(id);target.replaceChildren();for(const descriptor of descriptors)target.append(field(...descriptor)); }
  renderRoutes();refreshRouteOptions();
}

function showLogin() { authenticated=false;clearTimeout(pollTimer);$('app').hidden=true;$('login').hidden=false; }
async function load() {
  try {
    const data=await request('/admin/config');
    settings=data.config;revision=data.revision;csrf=data.csrf_token;storedSecrets=data.stored_secrets;clearSecrets.clear();
    $('version').textContent='v'+data.version;authenticated=true;dirty=false;
    $('login').hidden=true;$('app').hidden=false;notice('',false,true);
    render();$('save-state').textContent=data.configured?'Alle Einstellungen geladen':'Noch nicht eingerichtet';
    notice(data.error||(!data.configured?'Trage zunächst die Reolink-Zugangsdaten und deine Telefonanlage ein. Der Passivmodus ist für den ersten Verbindungstest aktiviert.':''),Boolean(data.error));
    clearTimeout(pollTimer);poll();
  } catch(error) { if(error.status===401)showLogin();else notice(error.message,true, !authenticated); }
}

function updateCallControls() {
  const route=lastStatus?.routes?.find(route=>route.id===$('test-route').value);
  $('test-call').disabled=!route?.test_call_available;
  $('hangup').disabled=!lastStatus?.controls?.hangup_available;
}
async function poll() {
  if(!authenticated)return;
  try {
    const data=await request('/admin/status');lastStatus=data;
    const names={starting:'Startet',preparing:'Wird vorbereitet',idle:'Bereit',ringing:'Ruft an',dialing:'Ruft an',connected:'Im Gespräch',active:'Im Gespräch',connecting_media:'Audio wird verbunden',error:'Fehler',stopping:'Wird beendet',ending:'Legt auf'};
    $('gateway-state').textContent=names[data.gateway.state]||data.gateway.state;
    $('gateway-detail').textContent=data.gateway.dry_run?'Passivmodus aktiv':data.call.active?'Gespräch aktiv':'Anrufe aktiviert';
    $('trigger-state').textContent=data.gateway.trigger_connected?'Verbunden':'Warte auf Verbindung';
    $('trigger-detail').textContent=data.gateway.trigger_source==='manual'?'Manuelle Anrufsteuerung':'Reolink-Klingeltaste';
    $('sip-state').textContent=data.gateway.dry_run?'Passivmodus':data.sip.registered?'Registriert':'Nicht registriert';
    $('sip-detail').textContent=data.sip.last_registration_error||data.gateway.last_error||'FRITZ!Box / SIP';
    updateCallControls();
  } catch(error) {
    lastStatus=null;updateCallControls();
    if(error.status===401) { showLogin();return; }
    $('gateway-state').textContent='Einrichtung / Neustart';$('gateway-detail').textContent=error.message;
    $('trigger-state').textContent='—';$('sip-state').textContent='—';
  }
  pollTimer=setTimeout(poll,3000);
}

$('login-form').addEventListener('submit',async event=>{event.preventDefault();const button=event.submitter;button.disabled=true;try { await post('/login',{token:$('access-token').value});$('access-token').value='';await load(); } catch(error) { notice(error.message,true,true); } finally { button.disabled=false; }});
$('logout').addEventListener('click',async()=>{try { await post('/admin/logout',{});showLogin(); } catch(error) { notice(error.message,true); }});
for(const button of document.querySelectorAll('[data-tab]'))button.addEventListener('click',()=>{for(const other of document.querySelectorAll('[data-tab]'))other.classList.toggle('selected',other===button);for(const panel of document.querySelectorAll('[data-panel]'))panel.hidden=panel.dataset.panel!==button.dataset.tab;});
$('settings-form').addEventListener('submit',async event=>{
  event.preventDefault();
  if(lastStatus?.call?.active&&!confirm('Das Übernehmen beendet das laufende Gespräch. Jetzt übernehmen?'))return;
  $('save').disabled=true;
  try { await post('/admin/config',{config:settings,revision,clear_secrets:[...clearSecrets]});dirty=false;await load();notice('Gespeichert. Die Verbindungen werden mit den neuen Einstellungen aufgebaut.'); }
  catch(error) { notice(error.message,true); }
  finally { $('save').disabled=false; }
});
$('reload').addEventListener('click',()=>{if(!dirty||confirm('Ungespeicherte Änderungen verwerfen?'))load();});
$('add-route').addEventListener('click',()=>{let number=settings.call_routes.length+1;while(settings.call_routes.some(route=>route.id==='route_'+number))number++;settings.call_routes.push({id:'route_'+number,name:'Route '+number,visitor_entity:'',doorbell_number:'',mobile_number_1:'',mobile_number_2:'',mobile_number_3:''});markDirty();renderRoutes();refreshRouteOptions();});
$('test-route').addEventListener('change',updateCallControls);
$('test-call').addEventListener('click',async()=>{try { await post('/admin/test/'+encodeURIComponent($('test-route').value),{});notice('Testanruf gestartet.'); } catch(error) { notice(error.message,true); }});
$('hangup').addEventListener('click',async()=>{try { await post('/admin/hangup',{});notice('Gespräch wird beendet.'); } catch(error) { notice(error.message,true); }});
$('import-file').addEventListener('change',async event=>{
  const file=event.target.files[0];if(!file)return;
  try {
    if(file.size>128*1024)throw new Error('Die Konfigurationsdatei ist zu groß.');
    const imported=JSON.parse(await file.text());
    if(imported.schema_version!==2||!Array.isArray(imported.call_routes)||!['reolink','sip','audio','call','trigger','live_image','diagnostics'].every(key=>imported[key]&&typeof imported[key]==='object'))throw new Error('Bitte einen Konfigurationsexport von Version 2 auswählen.');
    if(dirty&&!confirm('Ungespeicherte Änderungen durch den Import ersetzen?'))return;
    settings=imported;clearSecrets.clear();render();markDirty();notice('Import geladen. Bitte prüfen und mit „Speichern & übernehmen“ aktivieren.');
  } catch(error) { notice(error.message,true); }
  finally { event.target.value=''; }
});
window.addEventListener('beforeunload',event=>{if(dirty){event.preventDefault();event.returnValue='';}});
load();
