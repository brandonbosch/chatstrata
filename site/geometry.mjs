// Four instances of one beveled square. Each triangle has a flat normal so
// the light catches the bevels, as it does on the Sprawl Commons mark.
export function slabMesh() {
  const vertices = [];
  const ring = (extent, y) => [[-extent,y,-extent],[extent,y,-extent],[extent,y,extent],[-extent,y,extent]];
  const rings = [ring(.99,.075),ring(1.08,.025),ring(1.08,-.045),ring(.99,-.075)];
  function triangle(a,b,c,bevel) {
    const ab = b.map((v,i)=>v-a[i]), ac = c.map((v,i)=>v-a[i]);
    let n = [ab[1]*ac[2]-ab[2]*ac[1],ab[2]*ac[0]-ab[0]*ac[2],ab[0]*ac[1]-ab[1]*ac[0]];
    const center = a.map((v,i)=>(v+b[i]+c[i])/3);
    if (n.reduce((s,v,i)=>s+v*center[i],0)<0) { [b,c]=[c,b]; n=n.map(v=>-v); }
    const length=Math.hypot(...n); n=n.map(v=>v/length);
    for (const p of [a,b,c]) vertices.push(...p,...n,bevel);
  }
  for(let i=0;i<4;i++) triangle([.04,.09,-.03],rings[0][i],rings[0][(i+1)%4],0);
  for(let r=0;r<3;r++) for(let i=0;i<4;i++) {
    const j=(i+1)%4;
    triangle(rings[r][i],rings[r+1][i],rings[r+1][j],r===1?.35:1);
    triangle(rings[r][i],rings[r+1][j],rings[r][j],r===1?.35:1);
  }
  triangle(rings[3][0],rings[3][1],rings[3][2],0);
  triangle(rings[3][0],rings[3][2],rings[3][3],0);
  return new Float32Array(vertices);
}

export function rotation(pitch,yaw) {
  const cx=Math.cos(pitch),sx=Math.sin(pitch),cy=Math.cos(yaw),sy=Math.sin(yaw);
  return new Float32Array([cy,sx*sy,-cx*sy,0,cx,sx,sy,-sx*cy,cx*cy]);
}

export function layout(width,height) {
  return width>=1120
    ? {x:width*.74,y:height*.43,scale:Math.min(width*.14,height*.21)}
    : {x:width*.5,y:height*.24,scale:Math.min(width*.235,height*.115)};
}
