const $=id=>document.getElementById(id),token=document.querySelector('meta[name="setup-token"]').content;
let busy=false,preparedPlanId=null;
function notice(text,error=false){$('notice').textContent=text;$('notice').className=error?'error':''}
async function api(path,data){const r=await fetch('/api/'+path,{method:data===undefined?'GET':'POST',headers:{'X-Setup-Token':token,'Content-Type':'application/json'},body:data===undefined?undefined:JSON.stringify(data)});const text=await r.text();let v;try{v=JSON.parse(text)}catch{throw Error(text)}if(!r.ok)throw Error(v.error||text);return v}
function updateApply(){ $('apply').disabled=busy||!preparedPlanId||!$('approve').checked }
function invalidatePreview(hideInventory=false){preparedPlanId=null;$('review').hidden=true;$('approve').checked=false;if(hideInventory)$('inventory').hidden=true;updateApply()}
async function run(message,fn){
 if(busy)return;busy=true;
 const controls=[...document.querySelectorAll('button,input,select,textarea')].map(n=>[n,n.disabled]);
 controls.forEach(([n])=>n.disabled=true);notice(message);
 try{await fn()}catch(e){notice(e.message,true)}finally{controls.forEach(([n,disabled])=>n.disabled=disabled);busy=false;updateApply();updateSelection()}
}
function el(tag,text,cls){const n=document.createElement(tag);if(text)n.textContent=text;if(cls)n.className=cls;return n}
function selected(name){return document.querySelector(`input[name="${name}"]:checked`).value}
function options(){return{mode:selected('mode'),project:$('project').value.trim(),sandbox:selected('sandbox'),gitAuth:$('gitAuth').checked,baseline:$('baseline').checked}}
function imports(){return{ids:[...document.querySelectorAll('[data-id]:checked')].map(n=>n.dataset.id),overwrites:[...document.querySelectorAll('[data-overwrite]:checked')].map(n=>n.dataset.overwrite)}}
function selectionKey(){return 'copilot-ready-excluded:'+token+JSON.stringify([selected('mode'),$('project').value.trim()])}
function excludedImports(){try{const ids=JSON.parse(sessionStorage.getItem(selectionKey())||'[]');return new Set(Array.isArray(ids)?ids.filter(id=>typeof id==='string'):[])}catch{return new Set()}}
// Ready items are selected unless deselected; others, such as the other copy of a skill, only when chosen.
function chosenImports(){try{const ids=JSON.parse(sessionStorage.getItem(selectionKey()+':chosen')||'[]');return new Set(Array.isArray(ids)?ids.filter(id=>typeof id==='string'):[])}catch{return new Set()}}
function rememberSelection(){try{const ids=excludedImports(),chosen=chosenImports();for(const n of $('items').querySelectorAll('[data-id]')){if(n.dataset.bulkEligible==='true'){if(n.checked)ids.delete(n.dataset.id);else ids.add(n.dataset.id)}else if(n.checked)chosen.add(n.dataset.id);else chosen.delete(n.dataset.id)}sessionStorage.setItem(selectionKey(),JSON.stringify([...ids]));sessionStorage.setItem(selectionKey()+':chosen',JSON.stringify([...chosen]))}catch{}}
function otherCopyChosen(n){return !!n.dataset.skill&&[...document.querySelectorAll('input[data-skill]')].some(o=>o!==n&&o.checked&&o.dataset.skill===n.dataset.skill)}
function updateSelection(){
 const boxes=[...$('items').querySelectorAll('[data-id]')],eligible=boxes.filter(n=>n.dataset.bulkEligible==='true'&&!otherCopyChosen(n));
 $('selectAll').disabled=busy||!eligible.some(n=>!n.checked);
 $('clearSelection').disabled=busy||!$('items').querySelector('input:checked');
 const count=boxes.filter(n=>n.checked).length;
 $('selectionCount').textContent=`${count} selected`;
 const manual=$('workItems').querySelectorAll('[data-id]:checked').length;
 $('workHint').textContent=manual?`${manual} selected here. Review these choices carefully.`:$('workItems').children.length?'Left out for now. Open to see why.':'Nothing needs work.';
 $('preview').replaceChildren(document.createTextNode(count?`Review ${count} selected item${count===1?'':'s'}`:'Review setup'),el('span','→'));
}
function selectImports(checked){
 if(busy)return;
 // Select all leaves a skill alone when another copy of it was chosen, so the choice stands and only one copy is selected.
 $('items').querySelectorAll(checked?'[data-bulk-eligible="true"]':'input[type="checkbox"]').forEach(n=>{if(!checked||!otherCopyChosen(n))n.checked=checked});
 rememberSelection();invalidatePreview();updateSelection();
}
function renderReport(r){
 invalidatePreview();$('inventory').hidden=false;$('sources').replaceChildren();
 for(const s of r.sources||[]){const pill=el('span',`${s.name}: ${s.present?'settings found':'no settings found'}`,'pill');pill.title=s.version||'Version not checked';$('sources').append(pill)}
 $('warnings').replaceChildren(...(r.warnings||[]).map(x=>el('p',x,'fine')));
 for(const id of ['readyItems','workItems','availableItems'])$(id).replaceChildren();
 const excluded=excludedImports(),chosen=chosenImports(),counts={ready:0,work:0,available:0};
 const labels={native:'Already available',convertible:'Ready to bring over','needs-review':'Needs a closer look',unsupported:'Keep in the original app',skipped:'Not selected'};
 for(const i of r.items||[]){
  const ready=i.selectable===true&&!i.conflict&&i.disposition==='convertible',group=i.disposition==='native'?'available':ready?'ready':'work';counts[group]++;
  const row=el('div',null,'item'),label=el('label',null,'check'),box=el('input');box.type='checkbox';box.disabled=!i.selectable;box.dataset.id=i.id;box.dataset.bulkEligible=String(ready);box.checked=ready?!excluded.has(i.id):i.selectable&&chosen.has(i.id);if(i.kind==='skill')box.dataset.skill=i.name.toLowerCase();box.onchange=()=>{if(box.checked&&box.dataset.skill)for(const o of document.querySelectorAll('input[data-skill]'))if(o!==box&&o.dataset.skill===box.dataset.skill)o.checked=false;rememberSelection();invalidatePreview();updateSelection()};
  const body=el('span');body.append(el('strong',`${i.name} · ${i.source}`),el('span',i.conflict&&i.selectable?'Choose whether to replace':labels[i.disposition]||i.disposition,'pill'));if(i.selectable){label.append(box,body);row.append(label)}else row.append(body);
  if(group==='work')row.append(el('p',i.conflict&&i.selectable?'An existing file would change. Review it and choose whether to replace it.':i.detail));
  if(i.note)row.append(el('p',i.note,'fine'));
  const d=el('details');d.append(el('summary','See what’s included'),el('p',i.detail));if(i.destination)d.append(el('p',i.destination,'fine'));if(i.preview)d.append(el('pre',i.preview));
  for(const f of i.files||[]){const file=el('details');file.append(el('summary',`${f.path} · ${f.size.toLocaleString()} bytes${f.executable?' · can run code':''}`),el('pre',f.preview||'Contents not shown.'));if(f.truncated)file.append(el('p','Preview shortened. Review the original file for full contents.','fine'));d.append(file)}
  row.append(d);if(i.conflict&&i.selectable){const l=el('label',null,'check'),c=el('input');c.type='checkbox';c.dataset.overwrite=i.id;c.onchange=()=>{invalidatePreview();updateSelection()};l.append(c,el('span','Replace the existing file and keep a backup.'));row.append(l)}$(group+'Items').append(row)
 }
 $('readyCount').textContent=`${counts.ready} item${counts.ready===1?'':'s'}`;
 $('workCount').textContent=`${counts.work} item${counts.work===1?'':'s'}`;
 $('workGroup').open=counts.ready===0&&counts.work>0;
 $('availableGroup').hidden=counts.available===0;$('availableTitle').textContent=`Already available (${counts.available})`;
 if(!counts.ready)$('readyItems').append(el('p',counts.available?'Your existing settings are already available. There’s nothing ready to copy.':'Nothing is ready to copy yet. Review what needs work, or continue with a fresh setup.','empty-group'));
 updateSelection();
}
let importTimer=null;
function importText(p){
 const plural=k=>`${k} conversation${k===1?'':'s'}`;
 if(p.running)return{import:`Importing your conversations in the background: ${p.done} of ${p.total} done. You can open Copilot now; leave Copilot Ready running until this finishes.`,show:`Adding your conversations to the Copilot app: ${p.showDone} of ${p.showTotal} done. Leave Copilot Ready running until this finishes.`,finish:'Your conversations are in the Copilot app. Installing your hooks and plugins…'}[p.phase]||'Working on your conversations…';
 // Failures also lists conversations added to the app with a problem; those aren't counted as failed.
 const notes=Math.max(0,(p.failures||[]).length-p.failed-p.showFailed);
 const openHint='Choose Open in the Copilot app below, then choose Allow for each one in the app. Each stays in the sidebar under its repository or folder.';
 const app=p.separate?' The Copilot app doesn’t read a separate Copilot profile, so continue them in the Copilot CLI with this profile.':p.showInApp?` ${p.shown===p.showTotal?'They’re':`${plural(p.shown)} ${p.shown===1?'is':'are'}`} ready for the Copilot app. ${p.shown?openHint:''}${p.showFailed?` ${plural(p.showFailed)} couldn’t be added to the app; see Setup details.`:''}${notes?` ${plural(notes)} ${notes===1?'was':'were'} added with a problem; see Setup details.`:''}`:' To use them in the Copilot app too, open Update, repair, or remove this setup and choose Show my imported conversations in the Copilot app.';
 // The maintenance action only adds earlier imports to the app, so it has no import count.
 if(!p.total)return app.trim();
 return `Imported ${plural(p.imported)} into Copilot.${p.skipped?` ${plural(p.skipped)} skipped: already imported or no messages.`:''}${p.failed?` ${plural(p.failed)} couldn’t be imported; see Setup details.`:''}${app}`;
}
function renderImports(p){
 clearTimeout(importTimer);const n=$('importStatus'),any=p?.total||p?.showTotal;n.hidden=!any;$('openInApp').hidden=true;$('importFailures').replaceChildren();if(!any)return;
 n.textContent=importText(p);
 for(const f of p.failures||[])$('importFailures').append(el('p',f,'fine'));
 // Opening sends the app's documented ghapp://sessions link; the app asks the user to allow each one.
 $('openInApp').hidden=!(!p.running&&p.showInApp&&p.shown);
 // Hooks and plugins installed after the conversations join the setup checks, so redraw them.
 if(p.running)importTimer=setTimeout(async()=>{try{const s=await api('state');if(!s.imports?.running&&s.result)renderResult(s.result);else renderImports(s.imports)}catch{importTimer=setTimeout(()=>renderImports(p),3000)}},1500);
}
// Updating the normal profile keeps its protection unless the user picks another option.
function syncProtection(reset){
 const existing=selected('mode')==='existing',pick=v=>document.querySelector(`input[name="sandbox"][value="${v}"]`).checked=true;
 $('preserveChoice').hidden=!existing;
 if(reset)pick(existing?'preserve':'sandbox');else if(!existing&&selected('sandbox')==='preserve')pick('sandbox');
}
// After Undo or Uninstall the finished setup is gone, so start over from the first step.
function showStart(){
 for(const id of ['setupIntro','configure'])$(id).hidden=false;
 for(const id of ['outcome','review','inventory'])$(id).hidden=true;
 document.querySelector('footer').hidden=false;clearTimeout(importTimer);
}
function renderRecovery(status){
 const ids=status?.transactions||[];$('recovery').hidden=!ids.length;
 $('recoveryActions').replaceChildren(...ids.map(id=>{const b=el('button',`Restore files from ${id.slice(0,8)}`,'secondary');b.onclick=()=>{if(confirm('Restore the backed-up files from this setup attempt? Files you have edited since then are kept.'))run('Restoring saved files…',async()=>{notice((await api('maintenance',{action:'recover',id,confirm:true})).message);renderRecovery((await api('state')).status)})};return b}));
}
function renderResult(r){
 for(const id of ['setupIntro','configure','inventory','review'])$(id).hidden=true;
 document.querySelector('footer').hidden=true;
 $('outcome').hidden=false;preparedPlanId=null;$('approve').checked=false;updateApply();notice('');
 const failures=(r.validation||[]).filter(c=>!['passed','not verified'].includes(c.status));
 $('completionTitle').textContent=failures.length?'Setup needs attention':'Setup complete';
 $('completionCopy').textContent=!failures.length&&r.mode!=='new'?'Your selected changes are applied. You can also use copilot in a new terminal.':'Your selected changes are applied.';
 $('completionMark').textContent=failures.length?'!':'✓';$('completionMark').classList.toggle('attention',!!failures.length);
 $('results').replaceChildren();renderImports(r.imports);
 if(failures.length){const warning=el('div',null,'validation-issues');warning.setAttribute('role','alert');warning.append(el('strong','A setup check needs attention'));for(const c of failures)warning.append(el('p',`${c.name}: ${c.status}. ${c.detail}`));$('results').append(warning)}
 $('checks').replaceChildren();$('diagnostics').open=false;
 for(const c of r.validation||[])if(!failures.includes(c))$('checks').append(el('p',`${c.name}: ${c.status}. ${c.detail}`,'fine'));
 for(const warning of $('warnings').children)$('checks').append(warning.cloneNode(true));
}
$('choose').onclick=()=>run('Opening your folder picker…',async()=>{const r=await api('folder',{});if(r.path){$('project').value=r.path;invalidatePreview(true);notice('Folder selected. Find your settings when you’re ready.')}else notice('Folder selection cancelled.')});
document.querySelectorAll('#configure input').forEach(n=>n.addEventListener('change',()=>{if(n.name==='mode'){$('baseline').checked=selected('mode')==='new';syncProtection(true)}invalidatePreview(true)}));
$('project').addEventListener('input',()=>invalidatePreview(true));
$('scan').onclick=()=>run('Looking for your settings. Nothing is being changed or started…',async()=>{invalidatePreview(true);renderReport(await api('scan',options()));notice('Your setup is sorted. Deselect anything you don’t want, then review.');$('inventory').scrollIntoView({behavior:'smooth',block:'start'})});
$('selectAll').onclick=()=>selectImports(true);
$('clearSelection').onclick=()=>selectImports(false);
$('preview').onclick=()=>run('Preparing your changes…',async()=>{
 invalidatePreview();const chosen=imports(),p=await api('preview',chosen);
 if(typeof p.planId!=='string'||!p.planId)throw Error('These changes could not be prepared. Find your settings again.');
 preparedPlanId=p.planId;
 $('reviewTarget').replaceChildren(el('strong',p.mode==='existing'?'Updating your current Copilot setup':'Creating a separate Copilot setup'),el('span',p.profile));
 const protection={sandbox:'with extra protection',manual:'with normal access',preserve:'keeping your current protection setting'}[p.sandbox];
 $('summary').textContent=`We’ll bring over ${chosen.ids.length} selected item${chosen.ids.length===1?'':'s'} and set up Copilot ${protection}. The copilot command will be available in new terminal windows; existing commands keep their precedence${p.mode==='existing'?', and Open Copilot runs that same command':''}.`;
 $('settingChanges').replaceChildren(...(p.settings?.length?[el('strong','Copilot settings that will change'),...p.settings.map(s=>el('p',s,'fine'))]:[el('p','Your Copilot settings stay as they are.','fine')]));
 if(p.plugins)$('summary').textContent+=` ${p.plugins} plugin${p.plugins===1?'':'s'} will be installed with Copilot’s plugin command.`;
 if(p.conversations)$('summary').textContent+=` ${p.conversations} conversation${p.conversations===1?'':'s'} will import into Copilot in the background after setup; you can open Copilot without waiting.`;
 $('showInAppBox').hidden=!p.conversations||p.mode==='new';$('showInApp').checked=false;
 $('changes').replaceChildren();for(const c of p.changes){const n=el('div',null,'file');n.append(el('strong',c.path),el('span',`${c.action} · ${c.source} · ${c.bytes.toLocaleString()} bytes`));$('changes').append(n)}
 $('review').hidden=false;$('approve').checked=false;notice('Your changes are ready to review. Nothing has changed yet.');$('review').scrollIntoView({behavior:'smooth'})
});
$('approve').onchange=updateApply;
$('apply').onclick=()=>{if(!preparedPlanId||!$('approve').checked)return;const request={planId:preparedPlanId,options:options(),...imports(),showInApp:!$('showInAppBox').hidden&&$('showInApp').checked};run('Setting up Copilot and bringing over your choices. This may take a few minutes…',async()=>{try{const r=await api('apply',request);renderResult(r);$('completionTitle').focus();$('outcome').scrollIntoView({behavior:'smooth'})}catch(e){renderRecovery((await api('state').catch(()=>({}))).status);throw e}})};
$('launch').onclick=()=>run('Opening Copilot…',async()=>notice((await api('launch',{})).message));
$('powershell').onclick=()=>run('Installing Microsoft PowerShell 7. Watch for a Windows approval prompt…',async()=>{notice((await api('powershell',{})).message);$('powershellBox').hidden=true});
$('undo').onclick=()=>document.querySelector('[data-maint="undo"]').click();
$('openInApp').onclick=()=>run('Opening your conversations in the Copilot app…',async()=>notice((await api('maintenance',{action:'open',confirm:true,id:''})).message));
const confirmations={undo:'Undo the files your last setup changed? Backed-up files are restored and files setup created are removed. Files you edited since are kept, and imported conversations stay in Copilot.',repair:'Repair Copilot’s installed files? Your settings and project files will be kept.',rollback:'Switch to the previously installed Copilot version? Your settings and project files will be kept.',uninstall:'Remove Copilot Ready’s installed files and shortcuts? Your profiles and project files will be kept.',open:'Send your imported conversations to the Copilot app? The app asks Open session? for each one; choose Allow. Once all have been sent, choosing this sends them again, for any you dismissed.',show:'Show the conversations you imported in the Copilot app? Copilot continues each one once, on this computer: it gets one short exchange at its end (“Imported from … by Copilot Ready.” and “Ready to continue.”). This uses no AI credits and nothing leaves your computer. Copilot’s session index records them as updated today, and hooks already in your Copilot may run once for each one. Then the Copilot app opens them, newest first; choose Allow for each one there.'};
document.querySelectorAll('[data-maint]').forEach(b=>b.onclick=()=>{const action=b.dataset.maint;if(confirmations[action]&&!confirm(confirmations[action]))return;run(action==='show'?'Adding your imported conversations to the Copilot app…':action==='open'?'Opening your conversations in the Copilot app…':`Running ${action}…`,async()=>{const r=await api('maintenance',{action,confirm:true,id:''});notice(r.message);if(action==='undo'||action==='uninstall')showStart();if(r.url){const a=el('a','Open the release website');a.href=r.url;a.target='_blank';a.rel='noopener';$('notice').append(el('br'),a)}})});
run('Loading your setup…',async()=>{
 const s=await api('state'),o=s.options||{};
 if(['new','existing'].includes(o.mode)){
  document.querySelector(`input[name="mode"][value="${o.mode}"]`).checked=true;
  if(['sandbox','manual','preserve'].includes(o.sandbox))document.querySelector(`input[name="sandbox"][value="${o.sandbox}"]`).checked=true;
  $('baseline').checked=!!o.baseline;$('gitAuth').checked=!!o.gitAuth;
 }else $('gitAuth').checked=!!s.current?.gitAuth;
 syncProtection(false);
 renderRecovery(s.status);
 $('project').value=o.project||s.status?.launch?.project||'';
 $('existing').textContent=`Existing profile: ${s.existingProfile}`;$('managedRoot').textContent=`Managed files: ${s.root}`;
 const p=s.prerequisites;$('sandboxHint').textContent=p.sandboxHint||'Verify sandbox support in the native session.';$('powershellBox').hidden=!p.needsPowerShell;
 if(s.report)renderReport(s.report);
 if(s.result){renderResult(s.result);renderImports(s.imports)}else{
  notice(s.report?'Your scan is ready. Your deselections are remembered; review before continuing.':'Choose your setup and project to begin.');
  if(p.sandboxEligibilityKnown&&!p.sandboxEligible){$('advanced').open=true;notice('Extra protection needs a newer operating system. Choose normal access under Advanced to continue, or update your computer.',true)}
 }
 if(s.warning)notice(s.warning,true);
});
