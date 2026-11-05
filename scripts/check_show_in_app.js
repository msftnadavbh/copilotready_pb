// Run with playwright-cli run-code --filename=scripts/check_show_in_app.js
// against an isolated helper URL. All state, scan, preview, apply and maintenance calls are intercepted.
async page => {
 const assert=(ok,message)=>{if(!ok)throw Error(message)};
 const item=(id,extra={})=>({id,name:id,source:'claude',disposition:'convertible',selectable:true,detail:'Test only',...extra});
 const report={sources:[],items:[item('skill'),item('conversations',{kind:'conversations',name:'3 conversations'})]};
 let state={existingProfile:'Fixture profile',root:'Fixture root',prerequisites:{},options:{mode:'existing',project:'Fixture project',sandbox:'preserve'},report};
 let conversations=3;const applies=[],maintenance=[];
 const progress=(extra)=>({running:true,phase:'import',total:3,done:0,imported:0,skipped:0,failed:0,showInApp:true,showTotal:0,showDone:0,shown:0,showFailed:0,failures:[],...extra});
 await page.route('**/api/state',r=>r.fulfill({json:state}));
 await page.route('**/api/scan',r=>r.fulfill({json:report}));
 let mode='existing';
 await page.route('**/api/preview',r=>r.fulfill({json:{planId:'fixture-plan',mode,profile:'Fixture profile',sandbox:'preserve',changes:[],settings:[],conversations}}));
 await page.route('**/api/apply',r=>{applies.push(r.request().postDataJSON());const imports=progress({phase:'show',done:3,imported:3,showTotal:3,showDone:1});const result={validation:[],mode:'existing',imports};state={...state,result,imports};return r.fulfill({json:result})});
 await page.route('**/api/maintenance',r=>{maintenance.push(r.request().postDataJSON());return r.fulfill({json:{message:'3 of 3 imported conversations are ready for the Copilot app. Sent 3 conversations to the Copilot app, newest first.'}})});
 await page.reload();
 await page.locator('#scan').click();await page.locator('#preview:enabled').waitFor();
 await page.locator('#preview').click();await page.locator('#review:not([hidden])').waitFor();
 assert(await page.locator('#showInAppBox').isVisible(),'The option is missing for conversations');
 assert(!await page.locator('#showInApp').isChecked(),'The option must start off');
 const explanation=await page.locator('#showInAppBox').textContent();
 for(const words of ['Show these conversations in the Copilot desktop app','Ready to continue.','no AI credits','nothing leaves your computer','updated today','Hooks already in your Copilot may run once'])assert(explanation.includes(words),`The explanation does not say: ${words}`);
 await page.locator('#showInApp').check();await page.locator('#approve').check();await page.locator('#apply').click();
 await page.locator('#outcome:not([hidden])').waitFor();
 assert(applies[0].showInApp===true,'Apply did not carry the option');
 assert((await page.locator('#importStatus').textContent()).includes('Adding your conversations to the Copilot app: 1 of 3'),'Progress does not show the app step');
 state={...state,imports:progress({phase:'finish',done:3,imported:3,showTotal:3,showDone:3,shown:3})};
 await page.locator('#importStatus',{hasText:'Installing your hooks and plugins'}).waitFor();
 const done=progress({running:false,phase:'finish',done:3,imported:3,showTotal:3,showDone:3,shown:3});
 state={...state,imports:done,result:{...state.result,imports:done,validation:[{name:'Plugin helper',status:'passed',detail:'Installed in Copilot.'}]}};
 await page.locator('#importStatus',{hasText:'They’re ready for the Copilot app. Choose Open in the Copilot app below, then choose Allow for each one in the app.'}).waitFor();
 // The finish screen opens them in the app through its documented ghapp://sessions link.
 await page.locator('#openInApp').click();await page.locator('#notice',{hasText:'Sent 3 conversations'}).waitFor();
 assert(maintenance.at(-1)?.action==='open'&&maintenance.at(-1)?.confirm===true,'Open in the Copilot app did not ask the helper to open them');maintenance.length=0;
 assert(await page.locator('#checks').textContent().then(t=>t.includes('Plugin helper')),'Checks added after the conversations were not shown');
 await page.setViewportSize({width:375,height:900});await page.locator('#outcome').scrollIntoViewIfNeeded();
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'Mobile overflow');
 await page.screenshot({path:'build/show-in-app-finished.png'});
 // Without the option, the finish screen says how a conversation reaches the app.
 const off=progress({running:false,showInApp:false,done:3,imported:3});
 state={...state,imports:off,result:{validation:[],mode:'existing',imports:off}};
 await page.reload();await page.locator('#importStatus',{hasText:'choose Show my imported conversations in the Copilot app'}).waitFor();
 assert(!await page.locator('#openInApp').isVisible(),'Open in the Copilot app offered for conversations not added to the app');
 // A separate profile isn't sent to maintenance Show, which the app wouldn't read.
 const separate=progress({running:false,showInApp:false,separate:true,done:3,imported:3});
 state={...state,imports:separate,result:{validation:[],mode:'new',imports:separate}};
 await page.reload();await page.locator('#importStatus',{hasText:'doesn’t read a separate Copilot profile'}).waitFor();
 assert(!(await page.locator('#importStatus').textContent()).includes('Show my imported conversations'),'A separate profile is pointed to maintenance Show');
 // A conversation added with a problem is pointed out, and maintenance progress, which has no import count, still shows.
 const noted=progress({running:false,total:0,done:0,showTotal:2,showDone:2,shown:2,failures:['Added to the Copilot app with a problem: Fixture (Claude Code): Copilot shortened the conversation while adding it']});
 state={...state,imports:noted,result:{validation:[],mode:'existing',imports:noted}};
 await page.reload();await page.locator('#importStatus',{hasText:'1 conversation was added with a problem; see Setup details.'}).waitFor();
 assert(!(await page.locator('#importStatus').textContent()).includes('Imported 0'),'Maintenance progress claimed an import');
 assert((await page.locator('#importFailures').textContent()).includes('shortened'),'The problem is missing from Setup details');
 // No conversations, no option.
 conversations=0;state={...state,result:undefined,imports:undefined};await page.reload();
 await page.locator('#scan').click();await page.locator('#preview:enabled').waitFor();
 await page.locator('#preview').click();await page.locator('#review:not([hidden])').waitFor();
 assert(!await page.locator('#showInAppBox').isVisible(),'The option appeared without conversations');
 await page.evaluate(()=>{window.confirm=text=>{window.confirmed=text;return true}});
 await page.locator('#maintenance > summary').click();await page.locator('[data-maint="show"]').click();
 await page.locator('#notice',{hasText:'3 of 3 imported conversations'}).waitFor();
 assert(maintenance[0].action==='show'&&maintenance[0].confirm===true,'Maintenance action was not confirmed');
 const confirmed=await page.evaluate(()=>window.confirmed||'');
 for(const words of ['Ready to continue.','no AI credits','nothing leaves your computer','updated today','hooks already in your Copilot may run once','choose Allow for each one'])assert(confirmed.includes(words),`The confirmation does not say: ${words}`);
 // Maintenance can open them in the app too, without the finish screen.
 await page.locator('[data-maint="open"]').click();await page.locator('#notice',{hasText:'Sent 3 conversations'}).waitFor();
 assert((await page.evaluate(()=>window.confirmed||'')).includes('sends them again'),'Maintenance Open in the Copilot app sends again without asking');
 assert(maintenance.at(-1)?.action==='open','Maintenance Open in the Copilot app did not ask the helper to open them');
 // A separate profile, which the app doesn't read, gets no option.
 conversations=3;mode='new';await page.reload();
 await page.locator('#scan').click();await page.locator('#preview:enabled').waitFor();
 await page.locator('#preview').click();await page.locator('#review:not([hidden])').waitFor();
 assert(!await page.locator('#showInAppBox').isVisible(),'The option appeared for a separate profile');
 // Two copies of a skill: ticking one clears the other, and an identical copy has no Replace choice.
 report.items=[item('x',{kind:'skill',name:'other'}),item('a',{kind:'skill',name:'review',note:'The codex copy of this skill differs; this one is selected.'}),item('b',{kind:'skill',name:'review',source:'codex',disposition:'needs-review'}),item('c',{kind:'skill',name:'review',source:'shared',disposition:'native',selectable:false,conflict:true})];
 await page.reload();await page.locator('#scan').click();await page.locator('#preview:enabled').waitFor();
 assert(await page.locator('input[data-id="a"]').isChecked(),'The kept copy is not selected');
 assert((await page.locator('#readyItems').textContent()).includes('The codex copy of this skill differs'),'The kept copy’s row doesn’t say why');
 await page.locator('#workGroup > summary').click();await page.locator('input[data-id="b"]').check();
 assert(!await page.locator('input[data-id="a"]').isChecked(),'Both copies of one skill stayed selected');
 assert(await page.locator('#selectAll').isDisabled(),'Select all is offered with the other copy of a skill chosen');
 await page.locator('#clearSelection').click();await page.locator('input[data-id="b"]').check();await page.locator('#selectAll').click();
 assert(!await page.locator('input[data-id="a"]').isChecked()&&await page.locator('input[data-id="b"]').isChecked()&&await page.locator('input[data-id="x"]').isChecked(),'Select all selected both copies of one skill');
 assert(await page.locator('input[data-overwrite="c"]').count()===0,'An item that cannot be selected offers Replace');
 // The chosen copy is remembered across a reload, like deselections are.
 await page.reload();await page.locator('#scan').click();await page.locator('#preview:enabled').waitFor();
 assert(await page.locator('input[data-id="b"]').isChecked()&&!await page.locator('input[data-id="a"]').isChecked(),'The chosen copy was lost on reload');
 await page.setViewportSize({width:1280,height:900});
 await page.unrouteAll({behavior:'wait'});
 return {optionOffByDefault:true,explanation:true,applyCarriesOption:true,progress:true,finishTextOn:true,finishTextOff:true,lateChecks:true,mobile:true,hiddenWithoutConversations:true,maintenance:true,maintenanceOpen:true,separateProfile:true,separateFinish:true,skillCopies:true,rememberedCopy:true,installationPerformed:false};
}
