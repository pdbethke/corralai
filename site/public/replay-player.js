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
// #themebtn — and the replay-cockpit panels #exec, #tasks, #agents, #findings
// (when present, replay populates them from the tape; the canvas-only hero
// omits them and loses nothing).
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

// ---- view transform: wheel-zoom + drag-pan over the corral ----
// screen = world*viewScale + viewOffset. All node physics/layout stay in
// world coordinates (CW/CH space); draw() applies the transform once per
// frame via ctx.setTransform, and every consumer of a canvas mouse event
// must go through canvasToWorld() for hit-testing (see index.html).
let viewScale = 1, viewOX = 0, viewOY = 0;
const VIEW_MIN = 0.4, VIEW_MAX = 4;
let viewDidPan = false;   // set by a completed drag-pan; eats the click that follows
function screenToWorld(sx, sy){ return { x: (sx - viewOX)/viewScale, y: (sy - viewOY)/viewScale }; }
function canvasToWorld(ev){
  const r = cv.getBoundingClientRect();
  return screenToWorld(ev.clientX - r.left, ev.clientY - r.top);
}
function getViewTransform(){ return { scale: viewScale, x: viewOX, y: viewOY }; }
function resetView(){ viewScale = 1; viewOX = 0; viewOY = 0; }
function zoomAt(sx, sy, factor){
  const ns = Math.min(VIEW_MAX, Math.max(VIEW_MIN, viewScale * factor));
  // keep the world point under the cursor fixed: it maps to the same screen
  // px before and after the scale change, so the offset absorbs the delta.
  viewOX = sx - (sx - viewOX) * (ns / viewScale);
  viewOY = sy - (sy - viewOY) * (ns / viewScale);
  viewScale = ns;
}
cv.addEventListener('wheel', ev => {
  ev.preventDefault();
  dismissViewHint();
  const r = cv.getBoundingClientRect();
  // Trackpad pinch arrives as the SAME 'wheel' event with ctrlKey set (the
  // browser's own convention for "prevent page zoom, handle it yourself").
  // It tends to report much larger per-tick deltaY than a physical wheel
  // notch, so it gets a gentler coefficient to land at a comparable feel.
  const k = ev.ctrlKey ? 0.008 : 0.0015;
  zoomAt(ev.clientX - r.left, ev.clientY - r.top, Math.exp(-ev.deltaY * k));
}, { passive: false });
// drag-to-pan: only engages after a small movement threshold, so plain
// clicks (agent windows, inspector) keep working untouched. A completed pan
// fires a trailing 'click'; the hit-test in index.html ignores it by asking
// viewJustPanned() at the top of its handler (a shared flag, NOT a capture-
// phase propagation trick — that only worked by script load-order accident
// and a reorder/extra listener would silently break it).
let panFrom = null;
cv.addEventListener('mousedown', ev => { panFrom = { x: ev.clientX, y: ev.clientY, ox: viewOX, oy: viewOY }; });
addEventListener('mousemove', ev => {
  if(!panFrom) return;
  const dx = ev.clientX - panFrom.x, dy = ev.clientY - panFrom.y;
  if(!viewDidPan && Math.hypot(dx, dy) < 4) return;   // threshold: not a pan yet
  viewDidPan = true;
  dismissViewHint();
  viewOX = panFrom.ox + dx; viewOY = panFrom.oy + dy;
});
// Clear pan state on the GLOBAL mouseup — it always fires, even when the
// release lands OFF the canvas (routine: mousemove/mouseup are window-level
// so a pan continues past the edge, and the zoom controls sit right at that
// edge). A canvas 'click' only fires when mouseup also lands on cv, so
// clearing viewDidPan there leaked the flag when the release was off-canvas
// and silently ate the NEXT legit click. Deferring the reset to a macrotask
// keeps it true through the pan's OWN trailing click (dispatched right after
// this mouseup, before the timer) while a later unrelated click sees false.
addEventListener('mouseup', () => { panFrom = null; setTimeout(() => { viewDidPan = false; }, 0); });
// The one authority index.html's canvas hit-test consults to skip a pan's
// trailing click — robust to listener/registration order by construction.
function viewJustPanned(){ return viewDidPan; }
// double-click on EMPTY space resets to the 1x fit; a double-click on an
// agent stays the rename shortcut (index.html's own dblclick hit-test) —
// the two are disjoint by the same hit radius the rename handler uses.
// (No keyboard binding: neither file has canvas keyboard idioms to match.)
cv.addEventListener('dblclick', ev => {
  const p = canvasToWorld(ev);
  for(const n of nodes.values()){
    if(n.kind !== 'agent') continue;
    if(Math.hypot(n.x - p.x, n.y - p.y) < 22) return;  // rename territory — leave it alone
  }
  resetView();
});

// ---- touch: one-finger drag-pan, two-finger pinch-to-zoom ----
// A LinkedIn click from a phone/tablet has no wheel and no mouse, so touch
// gets its own gesture handling over the SAME transform + click-eater flag
// (a tap that ends a pan still fires a synthetic 'click', which the capture-
// phase listener above already eats via viewDidPan).
let touchMode = null;      // 'pan' | 'pinch' | null
let touchPanFrom = null;
let pinchStartDist = 0, pinchStartScale = 1;
function touchDist(a, b){ return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY); }
function touchMidCanvas(a, b){
  const r = cv.getBoundingClientRect();
  return { x: (a.clientX + b.clientX) / 2 - r.left, y: (a.clientY + b.clientY) / 2 - r.top };
}
cv.addEventListener('touchstart', ev => {
  dismissViewHint();
  if(ev.touches.length === 1){
    touchMode = 'pan';
    const t = ev.touches[0];
    touchPanFrom = { x: t.clientX, y: t.clientY, ox: viewOX, oy: viewOY };
  } else if(ev.touches.length === 2){
    touchMode = 'pinch';
    pinchStartDist = touchDist(ev.touches[0], ev.touches[1]) || 1;
    pinchStartScale = viewScale;
  }
}, { passive: true });
cv.addEventListener('touchmove', ev => {
  if(touchMode === 'pan' && ev.touches.length === 1){
    ev.preventDefault();
    const t = ev.touches[0];
    const dx = t.clientX - touchPanFrom.x, dy = t.clientY - touchPanFrom.y;
    if(!viewDidPan && Math.hypot(dx, dy) < 4) return;
    viewDidPan = true;
    viewOX = touchPanFrom.ox + dx; viewOY = touchPanFrom.oy + dy;
  } else if(touchMode === 'pinch' && ev.touches.length === 2){
    ev.preventDefault();
    const dist = touchDist(ev.touches[0], ev.touches[1]) || 1;
    const mid = touchMidCanvas(ev.touches[0], ev.touches[1]);
    const targetScale = pinchStartScale * (dist / pinchStartDist);
    zoomAt(mid.x, mid.y, targetScale / viewScale);
  }
}, { passive: false });
cv.addEventListener('touchend', ev => {
  if(ev.touches.length === 0){
    touchMode = null; touchPanFrom = null;
    // Same leak guard as the mouse path: a touch-pan sets viewDidPan but
    // preventDefault suppresses the synthetic mouseup that would clear it, so
    // clear it here (deferred, for symmetry) or the NEXT tap-to-open is eaten.
    setTimeout(() => { viewDidPan = false; }, 0);
  } else if(ev.touches.length === 1){
    // dropped from a pinch to a single finger — restart the pan baseline
    // from here rather than jumping using the old pinch anchor.
    touchMode = 'pan';
    const t = ev.touches[0];
    touchPanFrom = { x: t.clientX, y: t.clientY, ox: viewOX, oy: viewOY };
  }
});

// ---- on-screen zoom controls + discoverability hint ----
// Trackpad-less mice and plain touch taps have no wheel/pinch gesture, so a
// visible +/−/reset cluster is the accessible fallback — keyboard-focusable
// real <button>s with aria-labels, driving the IDENTICAL zoomAt/resetView
// transform (+/− anchor on the canvas CENTER, since there's no cursor to
// anchor on). Injected here, not in index.html/Hero.astro, so every embed
// (product/replay/site/observer) gets it for free from the shared renderer.
(function initViewControls(){
  const host = cv.parentElement;
  if(!host) return;
  if(getComputedStyle(host).position === 'static') host.style.position = 'relative';

  const style = document.createElement('style');
  style.textContent = `
/* Top-right, not bottom-right: index.html's #legend already owns the
   bottom-right corner of the canvas (bottom:8px; right:10px), and a
   z-index fight there would either hide the legend or crowd the buttons. */
.view-controls { position:absolute; right:10px; top:10px; z-index:5; display:flex; gap:6px; }
.view-controls button {
  width:28px; height:28px; padding:0; border-radius:6px; border:1px solid rgba(255,255,255,.25);
  background:rgba(20,20,20,.55); color:#eee; font-size:15px; line-height:1; cursor:pointer;
  display:flex; align-items:center; justify-content:center; backdrop-filter:blur(2px);
}
.view-controls button:hover, .view-controls button:focus-visible {
  background:rgba(40,40,40,.75); border-color:rgba(255,255,255,.5); outline:none;
}
/* No html.light override here on purpose — the stage stays dark in BOTH
   chrome themes (see index.html's #center), so these overlay controls keep
   their dark-glass styling regardless of the toggle. */
.view-hint {
  position:absolute; right:10px; top:44px; z-index:5; font-size:11px; color:#eee;
  background:rgba(20,20,20,.55); padding:3px 8px; border-radius:5px; pointer-events:none;
  opacity:1; transition:opacity 1.1s ease; white-space:nowrap;
}
.view-hint.hide { opacity:0; }
`;
  document.head.appendChild(style);

  const controls = document.createElement('div');
  controls.className = 'view-controls';
  controls.innerHTML =
    '<button type="button" aria-label="Zoom out">−</button>' +
    '<button type="button" aria-label="Reset zoom">⤢</button>' +
    '<button type="button" aria-label="Zoom in">+</button>';
  const [outBtn, resetBtn, inBtn] = controls.querySelectorAll('button');
  outBtn.title = 'Zoom out'; resetBtn.title = 'Reset view'; inBtn.title = 'Zoom in';
  outBtn.addEventListener('click', () => { dismissViewHint(); zoomAt(CW/2, CH/2, 1/1.3); });
  inBtn.addEventListener('click', () => { dismissViewHint(); zoomAt(CW/2, CH/2, 1.3); });
  resetBtn.addEventListener('click', () => { dismissViewHint(); resetView(); });
  host.appendChild(controls);

  const hint = document.createElement('div');
  hint.className = 'view-hint';
  hint.textContent = 'scroll or pinch to zoom · drag to pan';
  host.appendChild(hint);
  window.dismissViewHint = () => { hint.classList.add('hide'); };
  setTimeout(() => hint.classList.add('hide'), 4000);
})();
function dismissViewHint(){ if(window.dismissViewHint) window.dismissViewHint(); }

// ---- themed canvas backgrounds: grass (ranch/flock), honeycomb (hive) are
// pre-rendered offscreen; matrix rain animates in draw(). The stage is always
// dark (see index.html's #center — framed dark viewport in BOTH chrome
// themes), so these are tuned once for a dark ground and do NOT fork on the
// chrome's light/dark toggle.
const bgCv = document.createElement('canvas'); const bgCtx = bgCv.getContext('2d');
let rainDrops = [];
function renderBg(){
  bgCv.width = Math.max(1, CW); bgCv.height = Math.max(1, CH);
  bgCtx.clearRect(0,0,CW,CH);
  const kind = skin().bg;
  if(kind==='grass'){
    const rgb = '110,190,110';
    for(let i=0;i<Math.floor(CW*CH/9000);i++){
      const x = Math.random()*CW, y = Math.random()*CH, h = 6+Math.random()*9;
      bgCtx.strokeStyle = 'rgba('+rgb+','+(0.25+Math.random()*0.14)+')'; bgCtx.lineWidth=1.4;
      for(const dx of [-2,0,2]){
        bgCtx.beginPath(); bgCtx.moveTo(x,y); bgCtx.quadraticCurveTo(x+dx, y-h*0.6, x+dx*1.8, y-h); bgCtx.stroke();
      }
    }
  } else if(kind==='comb'){
    const rgb = '244,196,48';
    const r=16, w=r*Math.sqrt(3);
    bgCtx.strokeStyle='rgba('+rgb+',0.18)'; bgCtx.lineWidth=1.2;
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
  const rgb = '90,240,130';
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

// severity color/rank — shared by the live findings panel (index.html) and
// the replay cockpit's findings renderer below.
function sevColor(sev){ return (sev==='critical'||sev==='high')?C.red:(sev==='medium'?C.amber:'#6b6452'); }
const SEV_RANK = {critical:3, high:2, medium:1, low:0};

// ---- theme (light / dark) ----
let C = {};
function hexA(hex, a){ const h=(hex||'#888').replace('#','').trim(); const n=parseInt(h.length===3?h.replace(/./g,'$&$&'):h,16); return `rgba(${(n>>16)&255},${(n>>8)&255},${n&255},${a})`; }
// C drives EVERY swarm-canvas draw — agent labels, selection ring, conflict/
// quorum halos, claimed-path dots+labels, burst rings, speech/thought bubbles.
// The canvas is a permanently-dark stage in BOTH chrome themes (see #center),
// so C MUST source from the theme-invariant --stage-* palette, NOT the chrome
// --fg/--muted/… tokens. Reading the chrome set painted near-black label text
// and rings on the dark stage in light mode = invisible nodes. --stage-* is
// dark in both themes (never overridden by html.light), so labels/rings stay
// legible whichever chrome theme is active.
function readColors(){ const s=getComputedStyle(document.documentElement), g=k=>s.getPropertyValue(k).trim();
  C={fg:g('--stage-fg'),muted:g('--stage-muted'),amber:g('--stage-amber'),red:g('--stage-red'),line:g('--stage-line'),green:g('--stage-green'),panel:g('--stage-panel')}; }
function applyTheme(th){ document.documentElement.classList.toggle('light', th==='light');
  const b=document.getElementById('themebtn');
  if(b){ b.textContent = th==='light'?'☀':'☾'; b.setAttribute('aria-label', th==='light'?'Switch to dark theme':'Switch to light theme'); }
  readColors();
  try{ renderBg(); }catch(_){} }
function toggleTheme(){ const th=document.documentElement.classList.contains('light')?'dark':'light';
  try{ localStorage.setItem('corral-theme', th); }catch(_){} applyTheme(th); }
// Default: an explicit prior choice wins; otherwise follow the OS/browser's
// prefers-color-scheme on first load, falling back to dark if that API is
// unavailable (matches the chrome's own dark-first design).
applyTheme((()=>{ try{ return localStorage.getItem('corral-theme'); }catch(_){ return null; } })()
  || (()=>{ try{ return matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'; }catch(_){ return 'dark'; } })());
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
  // clear in DEVICE space (identity-ish base transform), THEN apply the
  // world transform once for everything that lives in the corral — the
  // background included (natural zoom: the pasture is part of the world;
  // beyond its edge the panel color shows, which reads as "the pasture
  // ends", not as a glitch). Per-node math stays untouched.
  ctx.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0);
  ctx.clearRect(0, 0, CW, CH);
  const frameT = Date.now()/1000, dt = Math.min(0.1, frameT-lastFrameT); lastFrameT = frameT;
  ctx.setTransform(devicePixelRatio*viewScale, 0, 0, devicePixelRatio*viewScale, devicePixelRatio*viewOX, devicePixelRatio*viewOY);
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

// ---- replay cockpit: the tape drives the console/tasks/findings panels ----
// The live panels render from lastState via apply() (SSE) — which is paused
// during replay, so in the product they used to freeze on stale live content
// while the canvas played the tape. These replay-side renderers accumulate
// state from applyReplayEvent and paint the SAME panel DOM (#exec, #tasks,
// #findings — all optional, null-guarded) from the tape instead. People want
// to see action: the whole viewport replays, not just the canvas.
// Honest limits of the tape: execution beats carry ok/exit_code but not the
// live feed's timed_out flag or multi-line output summary, and there is no
// recent_activity tool-call stream — the cockpit shows what was recorded,
// never invents the rest. The agents roster's "doing now" column is
// reconstructed from claims + executions (the tape has no agent_activity
// tool-call beat), so it shows the last real command or the claimed task,
// not a live tool call.
// replayConsoleLines: the console's single chronological feed — execution
// beats AND thought beats interleaved exactly as the tape emits them (both
// are appended here, in the same order applyReplayEvent processes the
// already-ts-sorted stream, so no separate ts-merge is needed: append order
// IS chronological order, for a full play-through and for a seek's
// rebuild-from-0 alike). Each entry is tagged `kind` so the renderer can
// style reasoning distinctly from action — see renderReplayConsole.
//   exec:    {kind:'exec', agent, role, command, ok, exitCode}
//   thought: {kind:'thought', agent, role, text}
let replayConsoleLines = [];
let replayTasks = new Map(); // key -> {key, title, role, status, claimedBy}
let replayFindings = [];    // {reporter, target, type, severity, model, resolved}
// name -> {name, role, held:Set<taskKey>, lastCmd, completed, lastTaskTitle,
//          lastTs, lastKind, lastDesc}. The trailing five fields (added for
//          the agent INSPECTOR WINDOW reconstruction, product-only — see
//          index.html's renderReplayAgentWindowBody) feed "completed",
//          "working on", and "last activity"; the roster (renderReplayAgents,
//          below) only ever reads held/lastCmd, so this is additive.
let replayAgents = new Map();
let replaySeenBeats = new Set(); // dedupe: findings ride the tape TWICE (queue+telemetry merge), and resolutions repeat across replan cycles
// replayExecFilter: isolate one actor's stream in the console (both #exec in
// the product and the site cockpit) — the tape-side twin of index.html's live
// `execFilter`/`setExecFilter`. NOT reset by resetReplayPanels(): it's a
// display filter over the accumulated beats, not accumulated state itself, so
// it must survive a scrub/seek's rebuild-from-0 (the whole point of the
// addendum — isolate BOB, then scrub around and stay isolated on BOB).
let replayExecFilter = '';
function setReplayExecFilter(n){ replayExecFilter = (replayExecFilter === n ? '' : n); renderReplayConsole(); }
function resetReplayPanels(){
  replayConsoleLines = []; replayTasks = new Map(); replayFindings = [];
  replayAgents = new Map(); replaySeenBeats = new Set();
}
function clearReplayPanelDOM(){
  // 'done' is the product's live roster id — cleared here too so the SSE
  // snapshot repaints it fresh on replay exit (no stale-tape flash), same as
  // the other panels; the site cockpit uses 'agents'.
  for(const id of ['exec','tasks','findings','agents','done']){
    const el = document.getElementById(id);
    if(el) el.innerHTML = '';
  }
}
function replayAgentEnsure(name, role){
  let a = replayAgents.get(name);
  if(!a){ a = {name, role: role || '', held: new Set(), lastCmd: '', completed: 0, lastTaskTitle: '', lastTs: 0, lastKind: '', lastDesc: ''}; replayAgents.set(name, a); }
  if(role) a.role = role;
  return a;
}
function replayAgentHold(name, role, taskKey, held){
  const a = replayAgentEnsure(name, role);
  if(held) a.held.add(taskKey); else a.held.delete(taskKey);
}
// replayAgentTouch: records the LATEST beat (by ts) for an actor, across
// every kind that counts as "activity" (claim/done/execution/thought) — feeds
// the inspector window's "last activity" line. Ties (equal ts) keep the
// newest-applied beat, which is what a rebuild-from-0 walk always wants.
function replayAgentTouch(name, role, ts, kind, desc){
  const a = replayAgentEnsure(name, role);
  if((ts||0) >= (a.lastTs||0)){ a.lastTs = ts||0; a.lastKind = kind; a.lastDesc = desc || ''; }
  return a;
}
// renderReplayAgents: the roster of workers the tape shows, reconstructed from
// task claims + executions — mirrors the product's live agents list (#done):
// role-colored dot when working (holds a claim), name + role + the "doing now"
// column (last real command, else the claimed task). System actors that only
// FILE findings (verify-gate, reflex-replanner, lead, client) never claim or
// run a command, so they never enter the roster — same as live active_agents.
function renderReplayAgents(){
  // The site cockpit names the roster #agents; the PRODUCT UI's live roster is
  // the legacy id #done (renders "agents · N" from apply()). Populate whichever
  // exists so replay drives the roster in BOTH hosts — in the product this also
  // fixes the roster going stale during replay (apply() is paused), exactly
  // like the console/tasks/findings panels.
  const ap = document.getElementById('agents') || document.getElementById('done');
  if(!ap) return;
  const ags = Array.from(replayAgents.values());
  if(!ags.length){ ap.innerHTML = '<div class="feedhdr">agents · 0</div><div class="row" style="opacity:.6">no agents yet…</div>'; return; }
  ags.sort((a,b)=>((b.held.size>0?0:1)-(a.held.size>0?0:1)) || ((displayName(a.name)>displayName(b.name))?1:-1));
  ap.innerHTML = '<div class="feedhdr">agents · ' + ags.length + '</div>' +
    ags.map(a => {
      const work = a.held.size > 0;
      const dot = work ? roleColor(a.role) : '#5b5750';
      const doing = work
        ? (a.lastCmd ? '❯ ' + esc(a.lastCmd.slice(0,22)) : 'on ' + esc(Array.from(a.held)[0] || ''))
        : 'idle';
      return '<div class="arow"><span class="adot" style="background:' + dot + '"></span><b style="color:' + roleColor(a.role) + '">' + esc(displayName(a.name)) + '</b> <span class="ameta">' + esc(a.role || '') + '</span><span class="adoing">' + doing + '</span></div>';
    }).join('');
}
// renderReplayLine: one console row, action or reasoning. Actions render
// exactly as before (❯ prompt, agent, command, ✓/✗ exit badge). Thoughts
// render VISIBLY DIFFERENT — a 💭 affix, no prompt/badge chrome, italic
// muted text — so a viewer instantly tells "the herd is reasoning" from
// "the herd just ran something" at a glance, never confusing the two.
// The thought text is rendered VERBATIM from the tape (only esc()'d for
// HTML safety) — never truncated, rewritten, or summarized here; that
// invariant is the whole point of the story engine (see internal/brain/
// thought.go's header comment).
function renderReplayLine(e){
  if(e.kind === 'thought'){
    return '<div class="xblk xthought"><div class="xcmdline xthoughtline">' +
      '<span class="xthoughtico" title="reasoning, not an action">💭</span> ' +
      '<b style="color:' + roleColor(e.role) + '">' + esc(displayName(e.agent)) + '</b> ' +
      '<span class="xthoughttext">·thinking· ' + esc(e.text || '') + '</span></div></div>';
  }
  const badge = e.ok
    ? '<span class="xbadge" style="color:var(--green)" title="exit 0">✓</span>'
    : '<span class="xbadge" style="color:var(--red)" title="exit ' + esc(String(e.exitCode)) + '">✗' + esc(String(e.exitCode)) + '</span>';
  return '<div class="xblk"><div class="xcmdline"><span class="xprompt">❯</span> <b style="color:' + roleColor(e.role) + '">' + esc(displayName(e.agent)) + '</b> <code class="xcmd">' + esc(e.command || '') + '</code> ' + badge + '</div></div>';
}
// replay filter chips: "all" + one per actor seen SO FAR in the accumulated
// console feed (execution AND thought beats alike, both tagged `agent`) —
// the tape-side twin of index.html's live chip() in renderExec(). Isolating
// one actor is how a busy multi-agent tape actually reads as a story: BOB's
// chip shows BOB's 💭 reasoning interleaved with BOB's ❯ commands, nothing
// from TESS.
function replayConsoleChip(name, role, on){
  return '<span class="xchip" onclick="setReplayExecFilter(\'' + name.replace(/'/g,"\\'") + '\')" style="cursor:pointer;padding:1px 6px;margin-left:5px;border-radius:8px;font-size:10.5px;' +
    (on ? 'background:' + (role?roleColor(role):'#6b6452') + ';color:#15110a;font-weight:700'
        : 'color:' + (role?roleColor(role):'var(--muted)') + ';border:1px solid ' + hexA(role?roleColor(role):'#6b6452', .5)) +
    '">' + esc(displayName(name)) + '</span>';
}
function renderReplayConsole(){
  const ep = document.getElementById('exec');
  if(!ep) return;
  const seen = {}; const actors = [];
  replayConsoleLines.forEach(e => { if(e.agent && !seen[e.agent]){ seen[e.agent] = e.role || ''; actors.push({name: e.agent, role: e.role || ''}); } });
  if(replayExecFilter && !seen.hasOwnProperty(replayExecFilter)) replayExecFilter = ''; // the isolated actor hasn't spoken yet at this scrub position
  const allChip = '<span class="xchip" onclick="setReplayExecFilter(\'\')" style="cursor:pointer;padding:1px 6px;margin-left:8px;border-radius:8px;font-size:10.5px;' +
    (replayExecFilter ? 'color:var(--muted);border:1px solid ' + hexA('#6b6452', .5) : 'background:#6b6452;color:#15110a;font-weight:700') + '">all</span>';
  const chips = allChip + actors.map(a => replayConsoleChip(a.name, a.role, replayExecFilter === a.name)).join('');
  const filtered = replayExecFilter ? replayConsoleLines.filter(e => e.agent === replayExecFilter) : replayConsoleLines;
  const tail = filtered.slice(-24);
  ep.innerHTML = '<div class="feedhdr">console · replaying the tape · ' + (replayExecFilter ? esc(displayName(replayExecFilter)) + ' · ' + filtered.length : replayConsoleLines.length) + ' ' + chips + '</div>' +
    (tail.length ? '' : '<div class="xempty">▌ no commands on the tape yet' + (replayExecFilter ? ' from ' + esc(displayName(replayExecFilter)) : '') + '…</div>') +
    tail.map(renderReplayLine).join('') + '<div class="xcursor">▌</div>';
  ep.scrollTop = ep.scrollHeight; // tail like the live console
}
function renderReplayTasks(){
  const tp = document.getElementById('tasks');
  if(!tp) return;
  const tasks = Array.from(replayTasks.values());
  if(!tasks.length){ tp.innerHTML = ''; return; }
  const order = {claimed:0, queued:1, done:2, superseded:3, cancelled:4};
  const counts = tasks.reduce((m,t)=>{ m[t.status]=(m[t.status]||0)+1; return m; }, {});
  const hdr = ['claimed','queued','done','superseded','cancelled'].filter(s=>counts[s]).map(s=>counts[s]+' '+s).join(' · ');
  tp.innerHTML = '<div class="feedhdr">tasks · ' + tasks.length + (hdr ? ' &nbsp; ' + hdr : '') + '</div>' +
    tasks.slice().sort((a,b)=>(order[a.status]??9)-(order[b.status]??9)).slice(0,50).map(t => {
      const gone = (t.status==='cancelled' || t.status==='superseded');
      const dot = t.status==='done' ? C.green : (t.status==='claimed' ? '#5b9bd5' : '#6b6452');
      const who = t.claimedBy && !gone ? ' <span style="color:' + roleColor(t.role) + '">← ' + esc(displayName(t.claimedBy)) + '</span>' : '';
      const titleStyle = gone ? 'color:var(--muted);text-decoration:line-through' : 'color:var(--fg)';
      return '<div class="trow"' + (gone ? ' style="opacity:.6"' : '') + '><span class="tdot" style="background:' + dot + '"></span><b style="' + titleStyle + '">' + esc(t.title || t.key) + '</b> <span style="color:var(--muted)">' + esc(t.role || '') + '</span>' + who + '</div>';
    }).join('');
}
function renderReplayFindings(){
  const fp = document.getElementById('findings');
  if(!fp) return;
  if(!replayFindings.length){ fp.innerHTML = ''; return; }
  const open = replayFindings.filter(f => !f.resolved);
  const crit = open.filter(f => f.severity==='critical' || f.severity==='high').length;
  const hdr = 'findings · ' + open.length + ' open' + (crit ? ' &nbsp; <b style="color:var(--red)">⚠ ' + crit + ' high</b>' : '');
  fp.innerHTML = '<div class="feedhdr">' + hdr + '</div>' +
    open.slice().sort((a,b)=>(SEV_RANK[b.severity]??0)-(SEV_RANK[a.severity]??0)).slice(0,30).map(f => {
      const hi = (f.severity==='critical' || f.severity==='high') ? ' hi' : '';
      const tgt = f.target ? ' <span style="color:var(--fg)">' + esc(f.target) + '</span>' : '';
      const mdl = f.model ? ' <span style="color:#5a7a8a;font-size:10px;font-family:ui-monospace,monospace">[' + esc(f.model) + ']</span>' : '';
      return '<div class="frow' + hi + '"><span class="fsev" style="color:' + sevColor(f.severity) + '">' + esc(f.severity) + '</span> <span style="color:var(--muted)">' + esc(f.type) + '</span>' + tgt + ' <span style="color:#6b6452">· ' + esc(displayName(f.reporter)) + '</span>' + mdl + '</div>';
    }).join('');
}
// refreshReplayAgentWindows: re-paints any OPEN agent inspector windows from
// the reconstructed tape state at the current scrub position — the product-
// only floating windows (index.html's agentWindows/renderAgentWindowBody)
// live outside this shared file's DOM contract, so this is guarded to a
// no-op wherever they don't exist (the site cockpit, the canvas-only hero).
// renderAgentWindowBody itself checks `inReplay` and reads replayAgents/
// replayTasks (this file's state) rather than the live lastState snapshot —
// see index.html's renderReplayAgentWindowBody.
function refreshReplayAgentWindows(){
  if(typeof agentWindows === 'undefined' || typeof renderAgentWindowBody !== 'function') return;
  agentWindows.forEach((_, name) => renderAgentWindowBody(name));
}
function renderReplayPanels(){
  if(!inReplay) return; // the live page's panels belong to apply()/SSE
  renderReplayConsole();
  renderReplayTasks();
  renderReplayAgents();
  renderReplayFindings();
  refreshReplayAgentWindows();
}

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
    resetReplayPanels();
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
  // Relinquish the panels: clear replay content so the live snapshot (pushed
  // immediately on SSE reconnect) repaints from a blank panel, never leaving
  // a flash of stale TAPE content posing as live state. inReplay is already
  // false at this point, so no replay render can race the repaint.
  resetReplayPanels();
  clearReplayPanelDOM();
  // Any agent window opened DURING replay was rendered from the tape (never
  // fetched /api/agent — see index.html's openAgentWindow) and its
  // detailData is still null. Leaving replay without this would leave that
  // window stuck showing "fetching…" forever, since nothing else ever
  // triggers the live fetch. Kick it once per open window, symmetric with
  // fetchWindowDetail's own retry loop for a transient failure.
  if(typeof agentWindows !== 'undefined' && typeof fetchWindowDetail === 'function'){
    agentWindows.forEach((win, name) => { win.detailData = null; fetchWindowDetail(name); });
  }
  if(replaySSEPaused){ es = connectSSE(); replaySSEPaused = false; } // resume live: the events handler pushes a fresh snapshot immediately on connect
}
function closeReplay(){
  setView('swarm'); // setView runs stopReplaySession() for every non-replay view
}
function toggleReplayPlay(){
  // Pressing play while the playhead is parked at the very end restarts from
  // the top (replay again) instead of sitting dead on the last frame — same
  // rebuild-from-0 the scrub uses, so canvas + panels both reset.
  if(!replayPlaying && replayIdx >= replayEvents.length){
    replayIdx = 0;
    nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
    resetReplayPanels();
    renderReplayScrub();
  }
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
  resetReplayPanels();
  while(replayIdx < target && replayIdx < replayEvents.length) applyReplayEvent(replayEvents[replayIdx++]);
  renderReplayScrub();
  if(replayPlaying) replayTimer = setTimeout(replayStep, Math.max(16, 250 / replaySpeed));
}
// applyReplayEvent: the read-only translation from one ReplayEvent (brain.go's
// merged, sorted beat stream) into the live canvas's node/link/burst/buzz
// vocabulary.
function applyReplayEvent(ev){
  // ---- cockpit accumulation (panels) — before the canvas switch below ----
  const d = ev.detail || {};
  switch(ev.kind){
    case 'task_created':
      if(ev.subject) replayTasks.set(ev.subject, {key: ev.subject, title: d.title || '', role: d.role || '', status: 'queued', claimedBy: ''});
      break;
    case 'task_claimed': {
      if(ev.subject){
        const t = replayTasks.get(ev.subject) || {key: ev.subject, title: d.title || '', role: d.role || ''};
        t.status = 'claimed'; t.claimedBy = ev.actor || ''; t.role = d.role || t.role;
        replayTasks.set(ev.subject, t);
        if(ev.actor){
          replayAgentHold(ev.actor, d.role, ev.subject, true);
          const title = d.title || ev.subject;
          const a = replayAgentEnsure(ev.actor, d.role); a.lastTaskTitle = title;
          replayAgentTouch(ev.actor, d.role, ev.ts, 'claimed', title);
        }
      }
      break;
    }
    case 'task_done': case 'task_cancelled': case 'task_superseded': {
      if(ev.subject){
        const t = replayTasks.get(ev.subject);
        if(t) t.status = ev.kind.slice(5); // done | cancelled | superseded
        if(ev.actor){
          replayAgentHold(ev.actor, d.role, ev.subject, false);
          const title = (t && t.title) || ev.subject;
          if(ev.kind === 'task_done'){ const a = replayAgentEnsure(ev.actor, d.role); a.completed = (a.completed||0) + 1; }
          replayAgentTouch(ev.actor, d.role, ev.ts, ev.kind, title);
        }
      }
      break;
    }
    case 'execution': {
      // dedupe on (actor, command, second-rounded ts) — cheap insurance in
      // case a stream ever carries a beat from both merge sources.
      const k = 'x|' + (ev.actor||'') + '|' + (ev.subject||'') + '|' + Math.round(ev.ts);
      if(!replaySeenBeats.has(k)){
        replaySeenBeats.add(k);
        replayConsoleLines.push({kind: 'exec', agent: ev.actor || '', role: d.role || '', command: ev.subject || '', ok: !!d.ok, exitCode: d.exit_code == null ? '' : d.exit_code});
        if(replayConsoleLines.length > 200) replayConsoleLines.shift();
      }
      // roster: an execution proves the actor is a real worker + records the
      // last command (its "doing now"), even if the claim beat was missed.
      if(ev.actor){
        const a = replayAgentEnsure(ev.actor, d.role); a.lastCmd = ev.subject || a.lastCmd;
        replayAgentTouch(ev.actor, d.role, ev.ts, 'exec', (ev.subject||'') + (d.ok ? ' ✓' : ' ✗'));
      }
      break;
    }
    // thought: internal/brain/replay.go merges these straight from telemetry
    // (kind="thought", actor=agent, detail={role,text}, subject unused) — a
    // single source, never doubled like findings, so no dedupe key is
    // needed. Appended to the SAME console feed as execution beats, so the
    // rebuilt-at-scrub-T order interleaves reasoning with action exactly as
    // the herd produced it.
    case 'thought': {
      replayConsoleLines.push({kind: 'thought', agent: ev.actor || '', role: d.role || '', text: d.text || ''});
      if(replayConsoleLines.length > 200) replayConsoleLines.shift();
      if(ev.actor) replayAgentTouch(ev.actor, d.role, ev.ts, 'thought', d.text || '');
      break;
    }
    case 'finding_reported': {
      // Findings ride the tape TWICE (queue + telemetry merge) — dedupe the
      // ~2ms double on (subject, severity, type, second-rounded ts). Each
      // DISTINCT report is appended (the reflex re-planner legitimately
      // re-raises the same subject seconds later — that reopens a finding).
      const k = 'f|' + (ev.subject||'') + '|' + (d.severity||'') + '|' + (d.type||'') + '|' + Math.round(ev.ts);
      if(!replaySeenBeats.has(k)){
        replaySeenBeats.add(k);
        replayFindings.push({reporter: ev.actor || '', target: ev.subject || '', type: d.type || '', severity: d.severity || '', model: ev.model || '', resolved: false});
      }
      break;
    }
    case 'finding_resolved': {
      // Resolutions ALSO ride twice AND repeat across replan cycles — dedupe
      // the double on (subject, second-rounded ts), then resolve the OLDEST
      // still-open finding for that subject (FIFO create→resolve pairing).
      // This mirrors the product's live renderFindings, which shows only
      // status==='open' rows: a finding stays visible until its resolution
      // beat fires, and a re-raise brings it back.
      const rk = 'r|' + (ev.subject||'') + '|' + Math.round(ev.ts);
      if(!replaySeenBeats.has(rk)){
        replaySeenBeats.add(rk);
        for(let i = 0; i < replayFindings.length; i++){
          if(replayFindings[i].target === ev.subject && !replayFindings[i].resolved){ replayFindings[i].resolved = true; break; }
        }
      }
      break;
    }
  }
  // ---- canvas translation (unchanged below this line) ----
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
    // vocabulary for yet. thought is likewise a no-op HERE — its payoff is
    // the console feed (renderReplayConsole), handled above; the canvas has
    // no glyph for "an agent is thinking" (yet). Unknown future kinds land
    // here too — graceful degradation, never a thrown error.
    case 'task_created':
    case 'finding_resolved':
    case 'thought':
    default:
      break;
  }
}
function renderReplayScrub(){
  renderReplayPanels();
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

