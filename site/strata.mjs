import {slabMesh,rotation,layout} from './geometry.mjs';
import {vertex,fragment} from './shaders.mjs';

const root=document.documentElement;
const canvas=document.querySelector('#strata');
const background=document.querySelector('#background');
const bloom=document.querySelector('#bloom');
const bg=background.getContext('2d'), glow=bloom.getContext('2d');
const separate=document.querySelector('#separate'), motion=document.querySelector('#motion');
const state=document.querySelector('#state');
const reduced=matchMedia('(prefers-reduced-motion: reduce)');
const palettes={
  mint:{accent:'#9fcab7',rgb:'159,202,183',bg:'#060c0c',shadow:[.022,.065,.055],mid:[.33,.53,.44],high:[.74,.98,.86]},
  lavender:{accent:'#b9aff5',rgb:'185,175,245',bg:'#090810',shadow:[.045,.031,.085],mid:[.44,.38,.66],high:[.85,.8,1.]},
  ice:{accent:'#8cc8ff',rgb:'140,200,255',bg:'#060b11',shadow:[.022,.045,.085],mid:[.28,.46,.65],high:[.69,.87,1.]}
};
let palette='mint', paused=reduced.matches, expanded=false, manual=false;
let width=innerWidth,height=innerHeight,scene=layout(width,height),dpr=1;
let gl=null,program,vao,uniforms,frame=0,previous=0,time=0,spread=0;
let pointer={x:0,y:0},contextLost=false;
const stars=Array.from({length:65},(_,i)=>({x:Math.sin(i*127.1)*.5+.5,y:Math.sin(i*311.7)*.5+.5,r:i%5===0?1:.5}));

function setPalette(name) {
  if(!palettes[name]) return;
  palette=name; const p=palettes[name];
  root.style.setProperty('--accent',p.accent); root.style.setProperty('--accent-rgb',p.rgb);
  root.style.setProperty('--bg',p.bg);
  for(const [name,value] of [['top',p.high],['mid',p.mid],['base',p.shadow]])
    root.style.setProperty('--slab-'+name,`rgb(${value.map(v=>Math.round(v*255)).join(',')})`);
  document.querySelector('meta[name="theme-color"]').content=p.bg;
  document.querySelectorAll('[data-palette]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.palette===name)));
  try{localStorage.setItem('chatstrata-visual-palette',name);}catch{}
  drawBackground(); draw();
}

function initGL() {
  gl=canvas.getContext('webgl2',{alpha:true,antialias:true,powerPreference:'low-power'});
  if(!gl) return false;
  const compiled=[];
  try {
    for(const [type,source] of [[gl.VERTEX_SHADER,vertex],[gl.FRAGMENT_SHADER,fragment]]) {
      const shader=gl.createShader(type); compiled.push(shader);
      gl.shaderSource(shader,source); gl.compileShader(shader);
      if(!gl.getShaderParameter(shader,gl.COMPILE_STATUS)) throw Error('Shader compilation failed');
    }
    program=gl.createProgram(); compiled.forEach(s=>gl.attachShader(program,s)); gl.linkProgram(program);
    if(!gl.getProgramParameter(program,gl.LINK_STATUS)) throw Error('Shader linking failed');
    vao=gl.createVertexArray(); gl.bindVertexArray(vao);
    const buffer=gl.createBuffer(); gl.bindBuffer(gl.ARRAY_BUFFER,buffer); gl.bufferData(gl.ARRAY_BUFFER,slabMesh(),gl.STATIC_DRAW);
    for(const [index,size,offset] of [[0,3,0],[1,3,12],[2,1,24]]) {
      gl.enableVertexAttribArray(index); gl.vertexAttribPointer(index,size,gl.FLOAT,false,28,offset);
    }
    uniforms=Object.fromEntries(['Rotation','Scale','Offset','Spread','Time','Shadow','Mid','High'].map(n=>[n,gl.getUniformLocation(program,'u'+n)]));
    gl.enable(gl.DEPTH_TEST); gl.enable(gl.CULL_FACE);
    root.classList.add('webgl-ready');
    return true;
  } catch {
    if(program) gl.deleteProgram(program);
    gl=null; root.classList.remove('webgl-ready');
    return false;
  } finally {
    if(gl) compiled.forEach(s=>gl.deleteShader(s));
  }
}

function drawBackground() {
  if(!bg) return;
  bg.setTransform(dpr,0,0,dpr,0,0); bg.clearRect(0,0,width,height);
  const p=palettes[palette], radius=Math.max(scene.scale*2.2,150);
  const halo=bg.createRadialGradient(scene.x,scene.y,0,scene.x,scene.y,radius);
  halo.addColorStop(0,`rgba(${p.rgb},.075)`); halo.addColorStop(1,`rgba(${p.rgb},0)`);
  bg.fillStyle=halo; bg.fillRect(0,0,width,height);
  bg.fillStyle=`rgba(${p.rgb},.3)`;
  for(const star of stars){bg.beginPath();bg.arc(star.x*width,star.y*height,star.r,0,Math.PI*2);bg.fill();}
  bg.strokeStyle=`rgba(${p.rgb},.055)`; bg.lineWidth=.7;
  const horizon=height*.70;
  for(let i=-8;i<=8;i++){bg.beginPath();bg.moveTo(scene.x+i*25,horizon);bg.lineTo(scene.x+i*width*.12,height);bg.stroke();}
  for(let i=1;i<9;i++){const y=horizon+(height-horizon)*(i/8)**2;bg.beginPath();bg.moveTo(0,y);bg.lineTo(width,y);bg.stroke();}
}

function draw() {
  if(!gl||contextLost) return;
  gl.viewport(0,0,canvas.width,canvas.height); gl.clearColor(0,0,0,0);
  gl.clear(gl.COLOR_BUFFER_BIT|gl.DEPTH_BUFFER_BIT); gl.useProgram(program); gl.bindVertexArray(vao);
  gl.uniformMatrix3fv(uniforms.Rotation,false,rotation(.58+pointer.y*.07,.62+time*.065+pointer.x*.12));
  gl.uniform2f(uniforms.Scale,scene.scale*12/width,scene.scale*12/height);
  gl.uniform2f(uniforms.Offset,scene.x*2/width-1,1-scene.y*2/height);
  gl.uniform1f(uniforms.Spread,spread); gl.uniform1f(uniforms.Time,time);
  const p=palettes[palette];
  for(const n of ['Shadow','Mid','High']) gl.uniform3fv(uniforms[n],p[n.toLowerCase()]);
  gl.drawArraysInstanced(gl.TRIANGLES,0,90,4);
  if(glow){glow.clearRect(0,0,bloom.width,bloom.height);glow.drawImage(canvas,0,0,bloom.width,bloom.height);}
}

function resize() {
  width=innerWidth; height=innerHeight; dpr=Math.min(devicePixelRatio||1,1.75); scene=layout(width,height);
  for(const c of [canvas,background]){c.width=Math.round(width*dpr);c.height=Math.round(height*dpr);}
  bloom.width=Math.round(width*.25); bloom.height=Math.round(height*.25);
  drawBackground(); draw();
}

const smooth=t=>{t=Math.max(0,Math.min(1,t));return t*t*(3-2*t);};
function animate(now) {
  frame=0;
  if(paused||document.hidden||!gl||contextLost) return;
  const dt=previous?Math.min((now-previous)/1000,.05):0; previous=now; time+=dt;
  const phase=time%22;
  const target=manual?Number(expanded):smooth((phase-7)/3)*(1-smooth((phase-15)/3));
  spread+=(target-spread)*(1-Math.exp(-dt*3));
  state.textContent=spread<.03?'aligned':spread>.97?'separated':target>spread?'unfolding':'restacking';
  draw(); frame=requestAnimationFrame(animate);
}
function run() {
  cancelAnimationFrame(frame); frame=0; previous=0;
  if(!paused&&!document.hidden&&gl&&!contextLost) frame=requestAnimationFrame(animate);
  motion.textContent=paused?'resume motion':'pause motion'; motion.setAttribute('aria-pressed',String(paused));
}
function toggleLayers() {
  manual=true; expanded=!expanded; root.classList.toggle('expanded',expanded);
  separate.setAttribute('aria-pressed',String(expanded));
  separate.innerHTML=`${expanded?'restack the layers':'separate the layers'} <span aria-hidden="true">↕</span>`;
  if(paused||!gl){spread=Number(expanded);state.textContent=expanded?'separated':'aligned';draw();}
}

document.querySelector('#copy').addEventListener('click',async()=>{
  const button=document.querySelector('#copy'),command=document.querySelector('#command'),status=document.querySelector('#copy-status');
  try {
    await navigator.clipboard.writeText(command.textContent);
    button.textContent='copied';status.textContent='Install command copied.';
    setTimeout(()=>button.textContent='copy',2000);
  } catch {
    const selection=getSelection(),range=document.createRange();range.selectNodeContents(command);selection.removeAllRanges();selection.addRange(range);
    status.textContent='Command selected. Press Control C or Command C to copy.';
  }
});
separate.addEventListener('click',toggleLayers);
motion.addEventListener('click',()=>{paused=!paused;run();});
document.querySelectorAll('[data-palette]').forEach(b=>b.addEventListener('click',()=>setPalette(b.dataset.palette)));
canvas.addEventListener('pointermove',e=>{
  const near=Math.hypot(e.clientX-scene.x,e.clientY-scene.y)<scene.scale*1.7;
  canvas.classList.toggle('hot',near);
  if(!paused){pointer.x=(e.clientX/width-.5)*2;pointer.y=(e.clientY/height-.5)*2;}
});
canvas.addEventListener('pointerleave',()=>{pointer={x:0,y:0};canvas.classList.remove('hot');});
canvas.addEventListener('click',e=>{if(Math.hypot(e.clientX-scene.x,e.clientY-scene.y)<scene.scale*1.7)toggleLayers();});
canvas.addEventListener('webglcontextlost',e=>{e.preventDefault();contextLost=true;root.classList.remove('webgl-ready');if(glow)glow.clearRect(0,0,bloom.width,bloom.height);run();motion.hidden=true;});
canvas.addEventListener('webglcontextrestored',()=>{contextLost=false;motion.hidden=!initGL();resize();run();});
addEventListener('resize',resize);
document.addEventListener('visibilitychange',run);
reduced.addEventListener('change',()=>{paused=reduced.matches;run();});
document.querySelector('#copy').hidden=false;
document.querySelector('.scene-control').hidden=false;
document.querySelector('.palettes').hidden=false;
initGL(); motion.hidden=!gl;
try{setPalette(localStorage.getItem('chatstrata-visual-palette')||'mint');}catch{}
resize();run();
