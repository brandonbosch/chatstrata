export const vertex = `#version 300 es
precision highp float;
layout(location=0) in vec3 aPosition;
layout(location=1) in vec3 aNormal;
layout(location=2) in float aBevel;
uniform mat3 uRotation;
uniform vec2 uScale, uOffset;
uniform float uSpread, uTime;
out vec3 vNormal, vPosition, vLocal;
out float vBevel, vLayer;
void main(){
  float layer=float(gl_InstanceID), index=layer-1.5;
  float angle=index*uSpread*.14, c=cos(angle), s=sin(angle);
  mat3 twist=mat3(c,0.,-s,0.,1.,0.,s,0.,c);
  vec3 p=twist*aPosition;
  p.y+=index*(.30+uSpread*.44);
  p.xz+=vec2(sin(layer*2.1),cos(layer*1.7))*uSpread*.035;
  p=uRotation*p;
  p.y+=sin(uTime*.6)*.035;
  vPosition=p; vNormal=uRotation*twist*aNormal;
  vLocal=aPosition; vBevel=aBevel; vLayer=layer;
  float z=6.-p.z, near=.1, far=40.;
  gl_Position=vec4(p.xy*uScale+uOffset*z,z*(far+near)/(far-near)-2.*far*near/(far-near),z);
}`;

export const fragment = `#version 300 es
precision highp float;
in vec3 vNormal, vPosition, vLocal;
in float vBevel, vLayer;
uniform vec3 uShadow, uMid, uHigh;
out vec4 color;
void main(){
  vec3 n=normalize(vNormal), view=normalize(vec3(0.,0.,6.)-vPosition);
  vec3 key=normalize(vec3(-.6,1.,1.4)), fill=normalize(vec3(1.,.1,-.6));
  float light=max(dot(n,key),0.), rim=pow(1.-max(dot(n,view),0.),3.);
  float spec=pow(max(dot(n,normalize(key+view)),0.),48.);
  vec3 material=mix(uShadow,uMid,.2+light*.66);
  material+=uMid*max(dot(n,fill),0.)*.16;
  material*=.73+vLayer*.09;
  material+=uHigh*(spec*.52+rim*.17+vBevel*(.07+light*.11));
  // Faint etched cells on the top faces suggest an archive without using text.
  vec2 cell=abs(fract((vLocal.xz+1.)*4.)-.5);
  float grid=1.-smoothstep(.008,.022,min(cell.x,cell.y));
  float top=smoothstep(.06,.074,vLocal.y);
  material+=uHigh*grid*top*.035;
  float edge=smoothstep(.96,1.08,max(abs(vLocal.x),abs(vLocal.z)));
  material+=uHigh*edge*top*.12;
  color=vec4(material,1.);
}`;
