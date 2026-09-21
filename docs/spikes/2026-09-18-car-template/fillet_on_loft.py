import sys, time
from build123d import *
L,W,H,WB,TR,D,TW,RH,FO=4500,1900,1250,2700,1650,680,265,110,950
cs,cl=0.35,0.40
nose,tail,belt,roofw=0.45,0.75,0.62,0.72
if len(sys.argv)>1:
    exec(sys.argv[1])
xf=WB/2+FO
def station(t,top,wfrac,cab):
    y0=RH; hw=W/2*wfrac
    if cab:
        yb=belt*H
    else:
        yb=top-0.05*(top-y0)
    yt=top
    ys=y0+0.35*(yb-y0)
    rw=hw*(roofw if cab else 0.6)
    pts=[(-0.85*hw,y0),(0.85*hw,y0),(hw,ys),(0.97*hw,yb),(rw,yt),(-rw,yt),(-0.97*hw,yb),(-hw,ys)]
    x=xf-t*L
    pl=Plane(origin=(x,0,0),x_dir=(0,0,-1),z_dir=(1,0,0))
    w=Wire.make_polygon([pl.from_local_coords((p[0],p[1],0)) for p in pts])
    return Face(w)
S=[station(0,nose*H,0.78,False),station(0.12,(nose+0.1)*H,0.95,False),station(cs,belt*H*1.02,1,False),
   station(cs+0.25*cl,H,1,True),station(cs+0.75*cl,H,1,True),station(cs+cl,belt*H*1.03,1,False),
   station(0.9,tail*H,0.98,False),station(1,tail*H*0.95,0.9,False)]
t0=time.time()
body=loft(S)
print("loft vol",body.volume, time.time()-t0)
arches=[]
for x in (WB/2,-WB/2):
  for s in (1,-1):
    inner=TR/2-TW/2-30; outer=W/2+60
    c=Cylinder(D/2+30,outer-inner).rotate(Axis.X,90).translate((x,D/2,s*(inner+outer)/2))
    arches.append(c)
cut=body
for a in arches: cut=cut-a
print("cut vol",cut.volume, len(cut.solids()), time.time()-t0)
for r in (5,15,30,60):
  for label,shape in (("before",body),("after",cut)):
    try:
      f=fillet(shape.edges(),radius=r); print(label,"fillet",r,"ok",f.volume, f.is_valid(), time.time()-t0)
    except Exception as e: print(label,"fillet",r,"FAIL",type(e).__name__, str(e)[:80])
