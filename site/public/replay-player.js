// SPDX-License-Identifier: Elastic-2.0
// replay-player.js — the corral canvas renderer + the read-only mission
// replay player, shared verbatim between the product UI (internal/ui/web/
// index.html, loaded via <script src="/replay-player.js">) and the
// corralai.dev site (site/public/replay-player.js, a hash-checked copy —
// see scripts/sync-site-assets.sh). Extracted from index.html so a static
// site with no brain running can embed the identical player.
//
// DOM CONTRACT — the embedding page MUST provide:
//   <canvas id="c">                the render surface
//   #replay, #replay-playbtn, #replay-scrub, #replay-label, #replay-title
//                                   the replay control bar (see index.html's
//                                   markup for the exact structure/CSS)
//   a global function setView(v)   called with 'replay' on startReplay() and
//                                   with the previous view on closeReplay()/
//                                   stopReplaySession(); at minimum it must
//                                   toggle #replay's "show" class. index.html
//                                   keeps its full multi-tab setView(); a
//                                   standalone embed can supply a two-line
//                                   version — see docs/superpowers/plans/
//                                   2026-07-03-corralai-dev-site.md Task 4.
//                                   GOTCHA: if the embedding page's script
//                                   uses a templating mechanism that wraps
//                                   the script body in a closure (e.g.
//                                   Astro's `define:vars`), a plain
//                                   `function setView(v){}` declaration is
//                                   scoped to that closure, NOT global — this
//                                   file's own top-level `setView('replay')`
//                                   call will throw ReferenceError. Assign
//                                   `window.setView = function(v){...}`
//                                   explicitly in that case.
//
// Optional, null-guarded (safe to omit entirely): #empty, #stat, #skinsel,
// #skinsub, #tab-swarm, #tab-proposals, #proposals-badge, #tab-completed,
// #themebtn.
//
// To start a live SSE-driven view (the product page only — a static replay
// embed never calls this): `es = connectSSE();`
// connectSSE() additionally requires a global apply(state) function; static
// embeds must never call connectSSE().
// To play a replay (works with no brain at all): `startReplay(streamOrUrl)`
// where streamOrUrl is either a URL string (GETted as {events:[...]}) or an
// already-resolved {events:[...]} object/array. Replay is genuinely
// backend-free end to end: requestChatter() (the in-character speech bubbles)
// short-circuits to its canned-line fallback whenever `inReplay` is true, so
// no /api/chatter call is ever made while replaying — a static embed with no
// backend at all will never see a failed network request or console error.

const cv = document.getElementById('c'), ctx = cv.getContext('2d');
// The canvas now lives in the CENTER grid cell, so it sizes to ITS box (not the
// window). CW/CH are its CSS-pixel dimensions; all physics + drawing use them.
let CW = 800, CH = 600;
function resize(){
  const r = cv.getBoundingClientRect();
  CW = Math.max(1, r.width); CH = Math.max(1, r.height);
  cv.width = CW*devicePixelRatio; cv.height = CH*devicePixelRatio;
  ctx.setTransform(devicePixelRatio,0,0,devicePixelRatio,0,0);
}
addEventListener('resize', resize);
new ResizeObserver(resize).observe(cv);   // re-fit when the cell changes (tab switches, window)
resize();

// ---- themed canvas backgrounds: grass (ranch/flock), honeycomb (hive) are
// pre-rendered offscreen; matrix rain animates in draw(). Tuned to read clearly
// at a glance without overpowering the nodes; light theme gets darker strokes
// since the pale/neon colors that work on the dark theme wash out on cream.
const bgCv = document.createElement('canvas'); const bgCtx = bgCv.getContext('2d');
let rainDrops = [];
function isLightTheme(){ return document.documentElement.classList.contains('light'); }
function renderBg(){
  bgCv.width = Math.max(1, CW); bgCv.height = Math.max(1, CH);
  bgCtx.clearRect(0,0,CW,CH);
  const kind = skin().bg, light = isLightTheme();
  if(kind==='grass'){
    const rgb = light ? '46,110,46' : '110,190,110';
    for(let i=0;i<Math.floor(CW*CH/9000);i++){
      const x = Math.random()*CW, y = Math.random()*CH, h = 6+Math.random()*9;
      bgCtx.strokeStyle = 'rgba('+rgb+','+(0.25+Math.random()*0.14)+')'; bgCtx.lineWidth=1.4;
      for(const dx of [-2,0,2]){
        bgCtx.beginPath(); bgCtx.moveTo(x,y); bgCtx.quadraticCurveTo(x+dx, y-h*0.6, x+dx*1.8, y-h); bgCtx.stroke();
      }
    }
  } else if(kind==='comb'){
    const rgb = light ? '176,124,10' : '244,196,48';
    const r=16, w=r*Math.sqrt(3);
    bgCtx.strokeStyle='rgba('+rgb+','+(light?0.22:0.18)+')'; bgCtx.lineWidth=1.2;
    for(let row=0; row*1.5*r<CH+2*r; row++){
      for(let col=0; col*w<CW+w; col++){
        const cx = col*w + (row%2? w/2:0), cy = row*1.5*r;
        bgCtx.beginPath();
        for(let k=0;k<6;k++){ const a=Math.PI/3*k+Math.PI/6; const px=cx+r*Math.cos(a), py=cy+r*Math.sin(a); k?bgCtx.lineTo(px,py):bgCtx.moveTo(px,py); }
        bgCtx.closePath(); bgCtx.stroke();
      }
    }
  } else if(kind==='rain'){
    rainDrops = [];
    for(let i=0;i<Math.floor(CW/26);i++) rainDrops.push({x:i*26+6, y:Math.random()*CH, v:40+Math.random()*90});
  }
}
new ResizeObserver(()=>{ try{ renderBg(); }catch(_){} }).observe(cv);
const RAIN_GLYPHS = '01コラルｱｲｳｴｵｶｷｸ<>{}#$';
function drawRain(dt){
  bgCtx.clearRect(0,0,CW,CH); // rain redraws its own layer each frame
  bgCtx.font='13px ui-monospace,monospace';
  const rgb = isLightTheme() ? '10,110,50' : '90,240,130';
  for(const d of rainDrops){
    d.y += d.v*dt; if(d.y > CH+80) { d.y = -20; d.v = 40+Math.random()*90; }
    for(let k=0;k<8;k++){
      bgCtx.fillStyle = 'rgba('+rgb+','+(0.45 - k*0.055)+')';
      bgCtx.fillText(RAIN_GLYPHS[(Math.random()*RAIN_GLYPHS.length)|0], d.x, d.y - k*13);
    }
  }
}

const nodes = new Map();   // id -> {id,kind,label,x,y,vx,vy,last,conflict}
const links = [];          // {a,b,conflict}
const bursts = [];         // execution ring-bursts: {x,y,t0,ok,timed} — fired when a bee's real command lands
const buzzes = [];         // little "buzz.." sound-effect texts that float up from active bees
// ---- skins: pluggable critter themes. The corral metaphor is the default —
// workers are the herd being corralled, the brain wrangles them; claims render
// as lassos. 'hive' keeps the original hand-drawn bees for the nostalgic.
const SKINS = {
  ranch: { label:'🤠 ranch', brain:'🤠', worker:'🐴', roles:{scrum:'🐕', client:'🧑‍💼'}, sub:'🐎', subtitle:'— the corral', bg:'grass', tab:'corral', empty:'no agents in the corral yet', warming:'the herd is saddling up…',
           center:'the corral', lasso:true, favicon:'🤠', proposes:'the herd proposes:', proposalsTab:'proposals', completedTab:'completed', noun:'pony', nouns:'ponies',
           sounds:['neigh~','clop clop','*swish*','yeehaw~'],
           caught:['Caught me — no {cmd} on record!','Busted! Never ran {cmd}…','Can’t outride the brand 🤠','No proof, no pass — re-doing it'],
           brainTag:'the wrangler — MCP coordinator · task queue · verification gate', replay:'🤠 riding the tape back' },
  flock: { label:'🐑 flock', brain:'🐕', worker:'🐑', roles:{scrum:'🐕‍🦺', client:'🧑‍🌾'}, sub:'🐑', subtitle:'— the fold', bg:'grass', tab:'fold', empty:'no agents in the fold yet', warming:'the flock is gathering…',
           center:'the fold', lasso:false, favicon:'🐑', proposes:'the flock proposes:', proposalsTab:'proposals', completedTab:'completed', noun:'sheep', nouns:'sheep',
           sounds:['baa~','baaa..','*nibble*','baa!'],
           caught:['Caught me — no {cmd} on record!','Busted! Never ran {cmd}…','Fleeced! No {cmd} run…','No proof, no pass — re-doing it'],
           brainTag:'the sheepdog — MCP coordinator · task queue · verification gate', replay:'🐑 reliving the fold' },
  matrix:{ label:'🕶 matrix', brain:'👁️', worker:'🕶️', roles:{scrum:'📟', client:'🥄'}, sub:'🐇', subtitle:'— the construct', bg:'rain', tab:'construct', honorific:'Mr. ', empty:'no Agents in the construct yet', warming:'loading the construct…',
           center:'the construct', lasso:false, favicon:'🕶️', proposes:'the construct proposes:', proposalsTab:'anomalies', completedTab:'archive', noun:'Agent', nouns:'Agents',
           sounds:['0101..','⋮⋮⋮','*dodge*','follow the 🐇'],
           caught:['There is no {cmd} on record','Busted! Never ran {cmd}…','The gate sees all 👁️','No proof, no pass — re-doing it'],
           brainTag:'the architect — MCP coordinator · task queue · verification gate', replay:'🕶️ replaying the construct' },
  hive:  { label:'🐝 hive', brain:null, worker:null, roles:{}, sub:null, subtitle:'— the hive', bg:'comb', tab:'hive', empty:'no bees in the hive yet', warming:'the bees are warming up…',
           center:'the corral', lasso:false, favicon:'🐝', proposes:'the hive proposes:', proposalsTab:'proposals', completedTab:'completed', noun:'bee', nouns:'bees',
           sounds:['buzz..','bzz..','bzzz~','buzz~'],
           caught:['Caught me — no {cmd} on record!','Busted! Never ran {cmd}…','Can’t fake the buzz 🐝','No proof, no pass — re-doing it'],
           brainTag:'the queen — MCP coordinator · task queue · verification gate', replay:'🐝 replaying the hive' },
};
let skinName = localStorage.getItem('corral-skin') || 'ranch';
if(!SKINS[skinName]) skinName = 'ranch';
function skin(){ return SKINS[skinName]; }
function setSkin(n){
  if(!SKINS[n]) return;
  skinName = n; localStorage.setItem('corral-skin', n);
  const fav = document.querySelector('link[rel="icon"]');
  if(fav) fav.href = "data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><text y='.9em' font-size='90'>" + skin().favicon + "</text></svg>";
  const sel = document.getElementById('skinsel'); if(sel) sel.value = n;
  document.title = 'CorralAI ' + skin().subtitle;
  const sub = document.getElementById('skinsub'); if(sub) sub.textContent = skin().subtitle;
  const tab = document.getElementById('tab-swarm'); if(tab) tab.textContent = skin().tab;
  const ptab = document.getElementById('tab-proposals');
  if(ptab){ const badge = document.getElementById('proposals-badge');
    ptab.firstChild.textContent = skin().proposalsTab || 'proposals';
    if(badge && !ptab.contains(badge)) ptab.appendChild(badge); }
  const ctab = document.getElementById('tab-completed');
  if(ctab) ctab.textContent = skin().completedTab || 'completed';
  const emp = document.getElementById('empty'); if(emp) emp.textContent = skin().empty;
  const rtitle = document.getElementById('replay-title');
  if(rtitle) rtitle.textContent = skin().replay || SKINS.ranch.replay;
  try{ renderBg(); }catch(_){}
  try{ renderExec(); }catch(_){ /* repaint the console's idle line in the new skin's voice */ }
  try{ renderTopology(); }catch(_){ /* first paint may precede state init */ }
  try{ renderProposalsTab(); }catch(_){ /* first paint may precede state init */ }
  // No completed-tab re-render here: its pane content is skin-neutral (only
  // the tab label is flavored, set above), and re-rendering would refetch
  // /api/history for nothing.
}
function critterGlyph(role, sub){
  const s = skin();
  if(!s.worker) return null;                    // hive: vector bees
  return s.roles[role] || (sub ? s.sub : s.worker);
}
function skinSounds(){ return skin().sounds; }

// drawBubble: comic bubble behind a line of text. Speech = rounded rect with a
// pointed tail; thought = rounded rect with two trailing dots. Caught = red rim.
function drawBubble(x, y, w, h, alpha, caught, thought){
  const r = 8;
  ctx.beginPath();
  ctx.moveTo(x+r, y);
  ctx.arcTo(x+w, y,   x+w, y+h, r); ctx.arcTo(x+w, y+h, x,   y+h, r);
  ctx.arcTo(x,   y+h, x,   y,   r); ctx.arcTo(x,   y,   x+w, y,   r);
  ctx.closePath();
  ctx.fillStyle = hexA(C.panel || '#101318', .92*alpha);
  ctx.fill();
  ctx.strokeStyle = hexA(caught ? C.red : C.line, .9*alpha); ctx.lineWidth = caught ? 1.6 : 1; ctx.stroke();
  if(thought){
    ctx.fillStyle = hexA(C.line, .8*alpha);
    ctx.beginPath(); ctx.arc(x+6, y+h+5, 2.5, 0, 7); ctx.fill();
    ctx.beginPath(); ctx.arc(x+1, y+h+10, 1.5, 0, 7); ctx.fill();
  } else {
    ctx.beginPath();
    ctx.moveTo(x+10, y+h); ctx.lineTo(x+4, y+h+8); ctx.lineTo(x+18, y+h);
    ctx.closePath();
    ctx.fillStyle = hexA(C.panel || '#101318', .92*alpha); ctx.fill();
    ctx.strokeStyle = hexA(caught ? C.red : C.line, .9*alpha); ctx.stroke();
  }
}

// requestChatter: in-character, MODEL-GENERATED speech grounded in the agent's
// real latest activity ("you are Tess, a sheep in the flock; you just ran go
// test…"). Server-cached + rate-limited; canned role lines are the fallback
// when the narrator is off, AND the only path taken during replay (see
// `inReplay` below) — replaying a recorded mission must never touch a brain
// endpoint, matching startReplay()'s "EMBED-FRIENDLY BY CONSTRUCTION" promise
// that a static, backend-less embed never calls out to a live server. Client
// throttles to one fetch per agent per 45s outside replay.
const chatterAt = {};   // agent -> last fetch ts
function requestChatter(n){
  const nowS = Date.now()/1000;
  if(inReplay || (chatterAt[n.label] && nowS - chatterAt[n.label] < 45)){
    const lines = roleQuips(n.role);
    if(lines) buzzes.push({x:n.x+6, y:n.y-15, t0:nowS, txt:lines[(Math.random()*lines.length)|0], say:true, role:n.role, life:2.5});
    return;
  }
  chatterAt[n.label] = nowS;
  fetch('/api/chatter?agent='+encodeURIComponent(n.label)+'&skin='+encodeURIComponent(skinName))
    .then(r => r.ok ? r.json() : null)
    .then(j => {
      if(j && j.line) buzzes.push({x:n.x+6, y:n.y-15, t0:Date.now()/1000, txt:j.line, say:true, role:n.role, life:3.5});
      else {
        const lines = roleQuips(n.role);
        if(lines) buzzes.push({x:n.x+6, y:n.y-15, t0:Date.now()/1000, txt:lines[(Math.random()*lines.length)|0], say:true, role:n.role, life:2.5});
      }
    }).catch(()=>{});
}

// populate the skin selector + apply the persisted choice
(function(){
  const sel = document.getElementById('skinsel');
  if(sel){
    for(const k of Object.keys(SKINS)){
      const o = document.createElement('option'); o.value = k; o.textContent = SKINS[k].label; sel.appendChild(o);
    }
  }
  setSkin(skinName);
})();
// role-appropriate chatter, translated into EACH skin's own universe — not
// suppressed, translated (the corral's design intent: a builder still riffs
// on its name/role, just in-persona). 'hive' keeps the original bee puns
// verbatim; ranch/flock/matrix carry the same beats reworded so no skin ever
// leaks another skin's vocabulary. matrix's builder line is a fixed, exact
// beat ("Can we fix it, Mr. Anderson?") — a deliberate, non-random riff on
// Bob-the-Builder translated into the construct's honorific.
const ROLE_LINES = {
  ranch: {
    researcher:['trailing fresh sign','scouting the range','tracking down leads','riding the back forty'],
    designer:['laying out the corral','keeping the fence lines clean',"this brand's solid","what's the layout?"],
    builder:['Can we build it? Yes we can!','breaking this bronco in','one more rail set','nailing down the gate'],
    tester:['clean as a whistle','no burrs in this saddle','all green on the range','herd tests pass'],
    pentester:['found a real varmint here',"snuffing out that snake","that bite'll leave a mark",'watch for rattlers'],
    perf:['fast as a quarter horse','slow as molasses','56 ns/op — quick on the draw','riding hard'],
    integrator:['branding it all together','merging the herd','the corral connects','rebase incoming'],
    writer:['writing up the trail log','needs a README, partner','spelling it out plain','add an example'],
    reviewer:['looks good to ride','needs changes, partner','nit: keep it tidy','did you test this?'],
    lead:['to the corral!','re-routing the herd','ship it, cowpokes',"who's wrangling this?"],
    client:['make it sweeter','mighty fine work!','a bit more polish please','not quite ready to brand'],
  },
  flock: {
    researcher:['following the scent','grazing for answers','found good pasture','to the flock-mind!'],
    designer:['keeping the fold tidy','wool-even architecture','un-baa-lievably neat',"what's the fold shape?"],
    builder:['Can we fix it?','ewe-ilding…','another fence post set','one more sprint'],
    tester:['sweet, all green','no burrs in MY wool','this test is un-baa-coming','flock tests pass'],
    pentester:['found a real wolf here','fleece that vuln','that bite stings','baa careful with input'],
    perf:["the sheep's knees",'slow as wet wool','56 ns/op — shear speed','fast as a spring lamb'],
    integrator:['fold-ing it all together','merging the flock','the fold connects','rebase incoming'],
    writer:['documenting the fold','needs a README, ewe','spelling it baa by baa','add an example'],
    reviewer:['looks good to ewe','needs changes, lamb','nit: baa consistent','did you test this?'],
    lead:['to the fold!','re-routing the flock','ship it, sheep',"who's herding this?"],
    client:['make it sweeter','wool-good!','more baa-utiful please','not quite ripe'],
  },
  matrix: {
    researcher:['jacking into the archives','tracing the signal','found a clean thread','back to the source'],
    designer:['architecting the construct','keeping the code clean','this pattern holds',"what's the structure?"],
    builder:['Can we fix it, Mr. Anderson?','compiling the construct…','another module locked','one more cycle'],
    tester:['clean run, no glitches','no bugs in this simulation','all green in the matrix','construct tests pass'],
    pentester:['found a real exploit here','patching that vulnerability','that exploit stings','watch the code'],
    perf:['faster than a bullet dodge','slow as lagged frames','56 ns/op — bullet time','fast as the construct'],
    integrator:['compiling it all together','merging the construct','the signal connects','rebase incoming'],
    writer:['documenting the construct','needs a README, operator','spelling it out in code','add an example'],
    reviewer:['looks clean, approved','needs changes, operator','nit: keep it consistent','did you test this?'],
    lead:['to the construct!','re-routing the signal','ship it, operators',"who's tracing this?"],
    client:['make it sharper','impressive work!','a bit more refinement please','not quite there'],
  },
  hive: {
    researcher:['bee-lining to the docs','scouting for nectar','found a sweet source','to the hive-mind!'],
    designer:['honeycomb architecture','keep the comb clean','un-bee-lievably tidy',"what's the cell shape?"],
    builder:['Can we fix it?','bee-uilding…','another cell capped','one more sprint'],
    tester:['sweet, all green','no bugs in MY comb','this test is un-bee-coming','swarm tests pass'],
    pentester:['Found a real bug here guys','swat that vuln','that bug stings','bee careful with input'],
    perf:["the bee's knees",'slow as cold honey','56 ns/op — buzz-worthy','fast as wingbeats'],
    integrator:['comb-ining it all','merging the swarm','the hive connects','rebase incoming'],
    writer:['documenting the hive','needs a README, honey','spelling it bee by bee','add an example'],
    reviewer:['looks good to bee','needs changes, drone','nit: bee consistent','did you test this?'],
    lead:['to the hive!','re-routing the swarm','ship it, bees',"who's foraging this?"],
    client:['make it sweeter','honey-good!','more bee-utiful please','not quite ripe'],
  },
};
// roleQuips: the active skin's canned line set for a role, falling back to the
// ranch set (never hive — hive's bee puns must never leak into another skin)
// if the active skin is missing that role.
function roleQuips(role){
  const bySkin = ROLE_LINES[skinName] || ROLE_LINES.ranch;
  return bySkin[role] || ROLE_LINES.ranch[role];
}
let bobSeeded = false; const bobDone = new Set();  // Bob says "Yes we can…" when he finishes a task
// when the verify gate refuses a lazy completion it raises a verify-gate finding;
// the caught bee owns up out loud (honest — driven by the real finding, not random).
const caughtFindings = new Set();
// caught lines live per-skin (skin().caught) — the verify-gate call-out stays in character.
let selected = null, lastState = {active_agents:[],live_claims:[],recent_completed:[]};
// Per-viewer display aliases (localStorage): rename a bee in the GUI WITHOUT
// touching its real identity. The real name keys all coordination, telemetry, and
// recorded history; displayName() is purely cosmetic, applied only at render time.
let beeAlias = {}; try { beeAlias = JSON.parse(localStorage.getItem('beeAlias')||'{}') || {}; } catch(_){ beeAlias = {}; }
function displayName(real){ const n = (real && beeAlias[real]) || real; return n ? (skin().honorific||'')+n : n; }
function renameBee(real){
  if(!real) return;
  const v = prompt('Rename '+real+' — display only, this browser. Leave blank to reset:', beeAlias[real]||'');
  if(v===null) return;
  const t = v.trim();
  if(t && t!==real) beeAlias[real]=t; else delete beeAlias[real];
  try{ localStorage.setItem('beeAlias', JSON.stringify(beeAlias)); }catch(_){}
  renderInspector();
}
let serverNow = 0, parkedGrace = 300, stateClientT = 0;
function liveServerNow(){ return serverNow + (performance.now()/1000 - stateClientT); } // skew-free elapsed

// agents are tinted by their swarm role so builder/tester/pentester/reviewer are
// instantly distinguishable; roleless agents fall back to the default amber.
const ROLE_COLORS = {researcher:'#e6c84f', designer:'#4fc3d9', builder:'#5b9bd5', tester:'#5ec98a', pentester:'#e0913a', perf:'#d96fb0', integrator:'#7b8cde', writer:'#9fb06a', reviewer:'#b07cd6'};
function roleColor(role){ return ROLE_COLORS[role] || C.amber; }

// ---- theme (light / dark) ----
let C = {};
function hexA(hex, a){ const h=(hex||'#888').replace('#','').trim(); const n=parseInt(h.length===3?h.replace(/./g,'$&$&'):h,16); return `rgba(${(n>>16)&255},${(n>>8)&255},${n&255},${a})`; }
function readColors(){ const s=getComputedStyle(document.documentElement), g=k=>s.getPropertyValue(k).trim();
  C={fg:g('--fg'),muted:g('--muted'),amber:g('--amber'),red:g('--red'),line:g('--line'),green:g('--green'),panel:g('--panel')}; }
function applyTheme(th){ document.documentElement.classList.toggle('light', th==='light');
  const b=document.getElementById('themebtn'); if(b) b.textContent = th==='light'?'☀':'☾'; readColors();
  try{ renderBg(); }catch(_){} }
function toggleTheme(){ const th=document.documentElement.classList.contains('light')?'dark':'light';
  try{ localStorage.setItem('corral-theme', th); }catch(_){} applyTheme(th); }
applyTheme((()=>{ try{ return localStorage.getItem('corral-theme'); }catch(_){ return null; } })()||'dark');
function ensure(id, kind, label){
  let n = nodes.get(id);
  if(!n){ n = {id,kind,label, x: CW/2 + (Math.random()-.5)*200, y: CH/2 + (Math.random()-.5)*200, vx:0, vy:0, last:0, conflict:false, phase: Math.random()*6.283, dartCd: 0, working:false, trail: []}; nodes.set(id,n); }
  return n;
}

function esc(s){ return (s||'').replace(/[&<>"']/g, m=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[m])); }

// simple force layout
function step(){
  const arr = [...nodes.values()];
  const cx = CW/2, cy = CH/2;
  for(const n of arr){
    let fx=(cx-n.x)*0.0015, fy=(cy-n.y)*0.0015;     // gravity
    for(const m of arr){ if(m===n) continue;
      let dx=n.x-m.x, dy=n.y-m.y, d2=dx*dx+dy*dy+0.01, d=Math.sqrt(d2);
      const rep = 2600/d2; fx += dx/d*rep; fy += dy/d*rep; }   // repulsion
    n.vx=(n.vx+fx)*0.82; n.vy=(n.vy+fy)*0.82;
  }
  for(const l of links){
    let dx=l.b.x-l.a.x, dy=l.b.y-l.a.y, d=Math.sqrt(dx*dx+dy*dy)+0.01, f=(d-90)*0.01;
    l.a.vx+=dx/d*f; l.a.vy+=dy/d*f; l.b.vx-=dx/d*f; l.b.vy-=dy/d*f; }
  // busy bees never stand still: a continuous organic wander keeps every bee in
  // motion (layered sines = a wandering flight path), and a WORKING bee is frenetic
  // — bigger amplitude plus occasional forage "darts" to a new spot. Idle bees still
  // hover gently. Motion amplitude IS the honesty signal: the harder a bee moves,
  // the more it's actually doing.
  const tt = Date.now()/1000;
  for(const n of arr){
    if(n.kind==='agent'){
      const e = n.working ? 1 : 0.36, ph = n.phase||0;
      n.vx += (Math.sin(tt*2.3+ph) + 0.6*Math.sin(tt*5.7+ph*1.7)) * 0.17 * e;
      n.vy += (Math.cos(tt*1.9+ph*1.3) + 0.6*Math.sin(tt*4.9+ph*0.7)) * 0.17 * e;
      if(n.working && (n.dartCd=(n.dartCd||0)-1) <= 0){    // forage: zip to a new flower
        const a=Math.random()*6.283, k=1.6+Math.random()*1.9;
        n.vx+=Math.cos(a)*k; n.vy+=Math.sin(a)*k; n.dartCd=32+Math.random()*64;
      }
    }
    n.x+=n.vx; n.y+=n.vy;
  }
}

// drawBee: a little role-colored bee — striped body, flapping wings, antennae.
// The queen (the brain at the heart of the corral) gets a crown.
function drawBee(x, y, r, color, t, queen){
  const wf = 0.5 + 0.5*Math.abs(Math.sin(t*6));          // wing flap — a busy flutter
  ctx.fillStyle = hexA('#dbe9ff', .52); ctx.strokeStyle = hexA('#ffffff', .4); ctx.lineWidth = 0.7;  // wings
  ctx.beginPath(); ctx.ellipse(x - r*0.72, y - r*0.38, r*1.08*wf, r*0.62, -0.7, 0, 7); ctx.fill(); ctx.stroke();
  ctx.beginPath(); ctx.ellipse(x + r*0.72, y - r*0.38, r*1.08*wf, r*0.62,  0.7, 0, 7); ctx.fill(); ctx.stroke();
  ctx.save();                                            // striped body (clip)
  ctx.beginPath(); ctx.ellipse(x, y, r*0.92, r*1.2, 0, 0, 7); ctx.clip();
  ctx.fillStyle = color; ctx.fillRect(x - r*1.3, y - r*1.4, r*2.6, r*2.8);
  ctx.fillStyle = hexA('#1c1208', .82);
  for(const dy of [-0.5, 0, 0.5]) ctx.fillRect(x - r*1.3, y + dy*r*1.2 - r*0.16, r*2.6, r*0.32);
  ctx.restore();
  ctx.beginPath(); ctx.ellipse(x, y, r*0.92, r*1.2, 0, 0, 7);
  ctx.strokeStyle = hexA('#1c1208', .5); ctx.lineWidth = 1; ctx.stroke();
  ctx.beginPath(); ctx.arc(x, y - r*1.25, r*0.42, 0, 7); ctx.fillStyle = '#241a10'; ctx.fill(); // head
  ctx.strokeStyle = '#241a10'; ctx.lineWidth = 1;        // antennae
  ctx.beginPath(); ctx.moveTo(x - r*0.2, y - r*1.55); ctx.lineTo(x - r*0.55, y - r*2.1);
  ctx.moveTo(x + r*0.2, y - r*1.55); ctx.lineTo(x + r*0.55, y - r*2.1); ctx.stroke();
  if(queen){                                             // a little gold crown
    const cw = r*1.8, cy = y - r*2.15;
    ctx.fillStyle = '#f4c430';
    ctx.beginPath(); ctx.moveTo(x - cw/2, cy);
    ctx.lineTo(x - cw/2, cy - r*0.55); ctx.lineTo(x - cw*0.25, cy - r*0.12);
    ctx.lineTo(x, cy - r*0.78); ctx.lineTo(x + cw*0.25, cy - r*0.12);
    ctx.lineTo(x + cw/2, cy - r*0.55); ctx.lineTo(x + cw/2, cy);
    ctx.closePath(); ctx.fill();
  }
}

// drawCritter: the themed node renderer. Emoji skins get a role-colored halo
// ring (role color survives any skin) + a gently bobbing critter; the hive
// skin falls through to the original hand-drawn bee.
function drawCritter(x, y, r, color, t, isBrain, role, sub){
  const g = isBrain ? skin().brain : critterGlyph(role, sub);
  if(!g){ drawBee(x, y, r, color, t, isBrain); return; }
  ctx.beginPath(); ctx.arc(x, y, r*1.35, 0, 7);
  ctx.strokeStyle = hexA(color, .85); ctx.lineWidth = isBrain ? 2 : 1.5; ctx.stroke();
  const bob = Math.sin(t*3 + x)*r*0.12;
  ctx.font = (r*2.1) + 'px serif'; ctx.textAlign='center'; ctx.textBaseline='middle';
  ctx.fillText(g, x, y + bob); ctx.textAlign='left'; ctx.textBaseline='alphabetic';
}

let lastFrameT = Date.now()/1000;
function draw(){
  step();
  ctx.clearRect(0,0,CW,CH);
  const frameT = Date.now()/1000, dt = Math.min(0.1, frameT-lastFrameT); lastFrameT = frameT;
  if(skin().bg==='rain') drawRain(dt);
  if(bgCv.width>1) ctx.drawImage(bgCv, 0, 0, CW, CH);
  for(const l of links){
    let parkedLabel = null;
    if(l.parked){
      const remaining = parkedGrace - (liveServerNow() - (l.since||0));
      const open = remaining <= 0;
      ctx.strokeStyle = hexA(open ? C.red : C.amber, .85);
      ctx.lineWidth = 2; ctx.setLineDash(open ? [2,4] : [5,4]);
      parkedLabel = open ? 'lease open to peers' : 'parked ' + Math.ceil(remaining) + 's';
    } else if(l.sub){ ctx.strokeStyle=hexA(C.amber,.35); ctx.lineWidth=1; ctx.setLineDash([3,3]); }
    else if(skin().lasso && !l.sub && !l.conflict){
      // ranch skin: a claim is a thrown lasso — rope-colored line, loop at the file end
      ctx.strokeStyle = hexA('#c9a86a', .8); ctx.lineWidth = 1.4; ctx.setLineDash([]);
    }
    else { ctx.strokeStyle = l.conflict ? hexA(C.red,.7) : hexA(C.line,.7); ctx.lineWidth = l.conflict ? 2 : 1; ctx.setLineDash([]); }
    ctx.beginPath(); ctx.moveTo(l.a.x,l.a.y); ctx.lineTo(l.b.x,l.b.y); ctx.stroke();
    if(skin().lasso && !l.sub && !l.conflict && !l.parked){
      ctx.beginPath(); ctx.arc(l.b.x, l.b.y, 8, 0, 7);
      ctx.strokeStyle = hexA('#c9a86a', .75); ctx.lineWidth = 1.4; ctx.stroke();
    }
    if(parkedLabel){
      ctx.setLineDash([]);
      ctx.fillStyle = hexA(C.fg, .85); ctx.font='10px ui-sans-serif';
      ctx.fillText(parkedLabel, (l.a.x+l.b.x)/2 + 4, (l.a.y+l.b.y)/2 - 4);
    }
  }
  ctx.setLineDash([]);
  const now = Date.now()/1000, t = now;
  // the queen — the brain at the heart of the corral, with her crown
  if(nodes.size){
    const qx=CW/2, qy=CH/2;
    const qglow = 34 + 7*Math.sin(t*1.4);
    ctx.beginPath(); ctx.arc(qx,qy, qglow, 0, 7); ctx.fillStyle=hexA(C.amber,.10); ctx.fill();
    drawCritter(qx, qy, 16, C.amber, t*0.7, true);
    ctx.fillStyle=hexA(C.muted,.85); ctx.font='11px ui-sans-serif'; ctx.textAlign='center';
    ctx.fillText(skin().center, qx, qy+38); ctx.textAlign='left';
  }
  for(const n of nodes.values()){
    if(n.kind==='agent'){
      const sub = !!n.parent;                              // subagent = smaller child node
      const fresh = Math.max(0, 1-(now-n.last)/300);      // 0..1 recency
      const working = (n.status === 'working');           // actually doing work right now
      n.working = working;                                 // feed the busy-bee physics (next step)
      const core = sub ? 5 : 8;
      const ac = roleColor(n.role);
      // flight trail: a working bee leaves a fading honey-colored path so the eye
      // tracks its motion — the busiest bees draw the longest streaks.
      if(working){ n.trail.push(n.x, n.y); if(n.trail.length>20) n.trail.splice(0,2); }
      else if(n.trail.length){ n.trail.splice(0,2); }
      if(n.trail.length>=4){
        ctx.beginPath(); ctx.moveTo(n.trail[0], n.trail[1]);
        for(let k=2;k<n.trail.length;k+=2) ctx.lineTo(n.trail[k], n.trail[k+1]);
        ctx.strokeStyle=hexA(ac,0.20); ctx.lineWidth=2; ctx.stroke();
      }
      // glow: bright + pulsing when working, faint when idle — so the eye finds the busy bees
      const pulse = (sub?9:14) + (working ? 3*Math.sin(t*3 + n.x) : 0);
      ctx.beginPath(); ctx.arc(n.x,n.y, pulse + (working?6:1)*fresh, 0, 7);
      ctx.fillStyle = hexA(ac, working ? (0.16+0.22*fresh) : 0.05); ctx.fill();
      // idle critters dim out; only the working ones are full color and lit
      ctx.globalAlpha = working ? 1 : 0.38;
      drawCritter(n.x, n.y, core, ac, t + n.x*0.6, false, n.role, sub);
      ctx.globalAlpha = 1;
      // ONLY working agents talk — the animation tells the truth. Speech is
      // model-generated in-character from the agent's REAL latest activity
      // (see requestChatter); the canned role lines are the offline fallback.
      if(working && Math.random()<0.013){
        if(Math.random()<0.5){ requestChatter(n); }
        else buzzes.push({x:n.x+core*0.7, y:n.y-core*1.7, t0:now, txt:skinSounds()[(Math.random()*skinSounds().length)|0], life:1.5});
      }
      const dn = displayName(n.label); const lbl = n.role ? dn+' ['+n.role+']' : dn;
      const parkedNode = n.status === 'awaiting_approval';
      if(parkedNode){
        ctx.beginPath(); ctx.arc(n.x,n.y, core+4, 0, 7);
        ctx.strokeStyle=hexA(C.amber,.95); ctx.lineWidth=2; ctx.setLineDash([2,2]); ctx.stroke(); ctx.setLineDash([]);
      }
      ctx.fillStyle = working ? C.fg : hexA(C.fg,.42); ctx.font=(sub?'11px':'12px')+' ui-sans-serif';
      ctx.fillText((parkedNode?'⏸ ':'')+lbl, n.x+core+4, n.y+4);
    } else {
      // active-work pulse: a file glows green while the agent holding it is fresh
      const cfresh = Math.max(0, 1-(now-(n.claimLast||0))/180);
      if(n.conflict){
        // contested file: a pulsing red alarm so collisions (the clobber!) are unmistakable
        const cp = 9 + 4*Math.abs(Math.sin(t*4 + n.x));
        ctx.beginPath(); ctx.arc(n.x,n.y, cp+5, 0, 7); ctx.fillStyle = hexA(C.red, 0.18); ctx.fill();
        ctx.beginPath(); ctx.arc(n.x,n.y, cp, 0, 7); ctx.strokeStyle = hexA(C.red,.9); ctx.lineWidth = 2; ctx.stroke();
      } else if(cfresh>0.02){
        ctx.beginPath(); ctx.arc(n.x,n.y, 6 + 5*cfresh + 2.5*Math.sin(t*4+n.x), 0, 7);
        ctx.fillStyle = hexA(C.green, 0.10+0.28*cfresh); ctx.fill();
      }
      ctx.beginPath(); ctx.arc(n.x,n.y, 5, 0, 7);
      ctx.fillStyle = n.conflict ? C.red : (cfresh>0.02 ? C.green : C.muted); ctx.fill();
      ctx.fillStyle = n.conflict ? C.red : C.muted; ctx.font='11px ui-monospace,monospace'; ctx.fillText(n.label, n.x+9, n.y+3);
    }
    if(n===selected){ ctx.beginPath(); ctx.arc(n.x,n.y, n.kind==='agent'?(n.parent?12:17):11, 0, 7); ctx.strokeStyle=C.fg; ctx.lineWidth=1.5; ctx.stroke(); }
  }
  // execution bursts: an expanding ring blooms from a bee when its REAL command
  // lands — green on a passing build/test, red on failure, amber on timeout.
  for(let i=bursts.length-1; i>=0; i--){
    const b=bursts[i], age=now-b.t0;
    if(age>1.2 || age<0){ bursts.splice(i,1); continue; }
    const p=age/1.2, r=9+48*p, col=b.timed?C.amber:(b.ok?C.green:C.red);
    ctx.beginPath(); ctx.arc(b.x,b.y, r, 0, 7);
    ctx.strokeStyle=hexA(col,.6*(1-p)); ctx.lineWidth=3*(1-p)+0.5; ctx.stroke();
  }
  // active critters murmur — and SAY things in character. Spoken lines get a
  // speech bubble; ambient murmurs get a thought bubble (cloud + dot tail).
  for(let i=buzzes.length-1; i>=0; i--){
    const b=buzzes[i], life=b.life||1.5, age=now-b.t0;
    if(age>life || age<0){ buzzes.splice(i,1); continue; }
    const p=age/life, alpha=Math.min(1,(1-p)*1.4);
    if(b.say){
      const txt = '“'+b.txt+'”';
      ctx.font = b.caught ? 'bold 13px ui-sans-serif' : '12px ui-sans-serif';
      const w = ctx.measureText(txt).width;
      const bx = b.x + (b.caught?0:2*Math.sin(age*4)), by = b.y - 26*p;
      drawBubble(bx-8, by-16, w+16, 23, alpha, b.caught, false);
      ctx.fillStyle = hexA(b.caught ? C.red : roleColor(b.role), .96*alpha);
      ctx.font = b.caught ? 'bold 13px ui-sans-serif' : '12px ui-sans-serif';
      ctx.fillText(txt, bx, by);
    } else {
      ctx.font='italic 11px ui-sans-serif';
      const w = ctx.measureText(b.txt).width;
      const bx = b.x + 5*Math.sin(age*7), by = b.y - 20*p;
      drawBubble(bx-7, by-14, w+14, 20, alpha*0.8, false, true);
      ctx.fillStyle=hexA(C.amber, .8*alpha);
      ctx.font='italic 11px ui-sans-serif';
      ctx.fillText(b.txt, bx, by);
    }
  }
  requestAnimationFrame(draw);
}
requestAnimationFrame(draw);

// connectSSE: opens (or re-opens) the live /events stream. Factored out so
// closeReplay() can restore the exact same wiring it tears down — no
// duplicated onmessage/onerror pair to drift out of sync.
function connectSSE(){
  const src = new EventSource('/events');
  src.onmessage = e => { try { apply(JSON.parse(e.data)); } catch(_){} };
  src.onerror = () => { const s = document.getElementById('stat'); if (s) s.textContent = 'reconnecting…'; };
  return src;
}
let es = null; // lazy: the embedding page calls `es = connectSSE()` itself for live SSE
// (see the DOM-contract header above) — a static replay embed never does, so `es` stays
// null and every `if(es && ...)` guard below skips cleanly.

// ---- replay player ----
// Replays a mission's recorded beat stream through the SAME apply-adjacent
// canvas machinery (nodes/links/bursts/buzzes, drawn by the one draw() loop
// above) that the live SSE feed drives — positions are never recorded, they
// recompute via the live force layout exactly like a live agent would.
//
// EMBED-FRIENDLY BY CONSTRUCTION: startReplay(streamOrUrl) is the only entry
// point that touches replay state, and it takes either a URL (fetched with a
// plain GET — what the live corral UI uses: '/api/replay?mission='+id) OR an
// already-resolved {events:[...]} object / bare array. Nothing below this
// point calls a brain/SSE endpoint directly or assumes one exists — a static
// corralai.dev embed with no brain running can hand startReplay() a baked
// JSON file's contents and get the identical player. openReplay(missionId)
// is just the live-corral convenience wrapper around that URL form.
let replayEvents = [], replayIdx = 0, replayPlaying = false, replaySpeed = 1, replayTimer = null, replaySSEPaused = false;
// inReplay: true from startReplay() through stopReplaySession() — the single
// switch that keeps replay genuinely backend-free (see requestChatter above).
// Distinct from replaySSEPaused, which only means anything when a live SSE
// connection existed to pause; a static embed never has one, so inReplay is
// the flag that also works with no brain running at all.
let inReplay = false;

function startReplay(streamOrUrl){
  const load = (typeof streamOrUrl === 'string')
    ? fetch(streamOrUrl).then(r => r.json()).then(d => d.events || [])
    : Promise.resolve((streamOrUrl && streamOrUrl.events) || streamOrUrl || []);
  return load.then(events => {
    replayEvents = events;
    replayIdx = 0;
    replayPlaying = false;
    inReplay = true;
    if(replayTimer){ clearTimeout(replayTimer); replayTimer = null; }
    nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
    if(es && !replaySSEPaused){ es.close(); replaySSEPaused = true; } // live mode paused while replaying
    setView('replay');
    renderReplayScrub();
  });
}
// openReplay: the live-corral entry point — wraps startReplay with the
// concrete /api/replay?mission=N URL. Called from the Completed tab's
// ▶ replay button.
function openReplay(missionId){
  startReplay('/api/replay?mission=' + missionId).catch(()=>{});
}
// stopReplaySession: the idempotent replay teardown — stop the step timer,
// reset the play button, resume live SSE if this session paused it. Called
// by setView() on ANY navigation to a non-replay view, so switching tabs
// mid-replay can never orphan the session (timer running, SSE closed, no
// visible control). closeReplay() is just the exit button's path into it.
function stopReplaySession(){
  replayPlaying = false;
  inReplay = false;
  if(replayTimer){ clearTimeout(replayTimer); replayTimer = null; }
  const btn = document.getElementById('replay-playbtn');
  if(btn) btn.textContent = '▶ play';
  if(replaySSEPaused){ es = connectSSE(); replaySSEPaused = false; } // resume live: the events handler pushes a fresh snapshot immediately on connect
}
function closeReplay(){
  setView('swarm'); // setView runs stopReplaySession() for every non-replay view
}
function toggleReplayPlay(){
  replayPlaying = !replayPlaying;
  const btn = document.getElementById('replay-playbtn');
  if(btn) btn.textContent = replayPlaying ? '⏸ pause' : '▶ play';
  if(replayPlaying) replayStep();
}
function setReplaySpeed(x){ replaySpeed = x || 1; }
function replayStep(){
  if(replayIdx >= replayEvents.length){
    replayPlaying = false;
    const btn = document.getElementById('replay-playbtn'); if(btn) btn.textContent = '▶ play';
    return;
  }
  applyReplayEvent(replayEvents[replayIdx++]);
  renderReplayScrub();
  if(replayPlaying) replayTimer = setTimeout(replayStep, Math.max(16, 250 / replaySpeed));
}
// seekReplay: deterministic scrub — rebuild all canvas state from the start
// of the stream up to the target index. No incremental/differential seek;
// re-walking the whole prefix is what keeps a seek back and a seek forward
// produce byte-identical state to just having played there.
function seekReplay(target){
  if(replayTimer){ clearTimeout(replayTimer); replayTimer = null; }
  replayIdx = 0;
  nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
  while(replayIdx < target && replayIdx < replayEvents.length) applyReplayEvent(replayEvents[replayIdx++]);
  renderReplayScrub();
  if(replayPlaying) replayTimer = setTimeout(replayStep, Math.max(16, 250 / replaySpeed));
}
// applyReplayEvent: the read-only translation from one ReplayEvent (brain.go's
// merged, sorted beat stream) into the live canvas's node/link/burst/buzz
// vocabulary.
function applyReplayEvent(ev){
  const now = Date.now()/1000; // same clock draw()'s bursts/buzzes compare against
  const agentId = ev.actor ? 'a:'+ev.actor : null;
  const pathId = ev.subject ? 'p:'+ev.subject : null;
  const an = agentId ? nodes.get(agentId) : null;
  switch(ev.kind){
    case 'task_claimed': {
      if(!agentId) break;
      const a = ensure(agentId, 'agent', ev.actor);
      a.role = (ev.detail && ev.detail.role) || a.role || '';
      a.status = 'working'; a.last = now; a.working = true;
      if(pathId){
        const short = (ev.subject||'').split('/').filter(Boolean).pop() || ev.subject;
        const p = ensure(pathId, 'path', short);
        p.claimLast = now;
        links.push({a: a, b: p});
      }
      break;
    }
    case 'task_done': case 'task_cancelled': case 'task_superseded': {
      if(an){ an.status = 'idle'; an.working = false; }
      for(let i=links.length-1; i>=0; i--){ if(links[i].b && links[i].b.id === pathId) links.splice(i,1); }
      break;
    }
    case 'execution': {
      bursts.push({x: an ? an.x : CW/2, y: an ? an.y : CH/2, t0: now, ok: !!(ev.detail && ev.detail.ok)});
      break;
    }
    case 'finding_reported': {
      const sev = (ev.detail && ev.detail.severity) || '';
      const x = (an ? an.x : CW/2) + 8, y = (an ? an.y : CH/2) - 14;
      buzzes.push({x, y, t0: now, txt: (sev ? sev+': ' : '') + (ev.subject||''), life: 3.2});
      break;
    }
    // Graceful no-ops, deliberately: task_created has no visual because the
    // live view doesn't render unclaimed queue entries as nodes either;
    // finding_resolved and the telemetry-only kinds (proposal/review/
    // mission_completed/agent_activity) are ambience the canvas has no
    // vocabulary for yet. Unknown future kinds land here too — graceful
    // degradation, never a thrown error.
    case 'task_created':
    case 'finding_resolved':
    default:
      break;
  }
}
function renderReplayScrub(){
  // apply() (the SSE path) normally owns #empty's "no agents yet" caption —
  // but apply() never runs during replay (SSE paused) or in a static embed
  // (no SSE at all), so the replay pipeline refreshes it here, at the single
  // choke point every replay state change funnels through (startReplay,
  // replayStep, seekReplay). Null-guarded: an embed may not render #empty.
  const emp = document.getElementById('empty');
  if(emp) emp.style.display = nodes.size ? 'none' : 'flex';
  const scrub = document.getElementById('replay-scrub');
  if(!scrub) return;
  scrub.max = replayEvents.length;
  scrub.value = replayIdx;
  const label = document.getElementById('replay-label');
  if(label) label.textContent = replayIdx + ' / ' + replayEvents.length;
}

