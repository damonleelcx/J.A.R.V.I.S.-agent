import sys, time, json
from build123d import *
N = int(sys.argv[1]) if len(sys.argv) > 1 else 17
cars = {
 "default": dict(L=4500,W=1900,H=1250,WB=2600,RH=110,FO=900,cs=.33,cl=.42,nose=.42,belt=.62,tail=.78,roof=.74,er=.025),
 "long-hood-gt": dict(L=4900,W=1950,H=1300,WB=2850,RH=110,FO=950,cs=.5,cl=.36,nose=.42,belt=.62,tail=.6,roof=.45,er=.025),
 "long-low-hypercar": dict(L=5200,W=2150,H=1000,WB=2950,RH=60,FO=1100,cs=.22,cl=.36,nose=.3,belt=.62,tail=.85,roof=.74,er=.06),
 "short-tall-suv": dict(L=3900,W=1850,H=1950,WB=2350,RH=260,FO=780,cs=.2,cl=.6,nose=.55,belt=.55,tail=.96,roof=.92,er=.005),
}
def clamp(u): return min(max(u,0),1)
def ss(u): u=clamp(u); return u*u*(3-2*u)
def station(c, t):
    L,W,H,RH=c["L"],c["W"],c["H"],c["RH"]
    cs,cl=c["cs"],c["cl"]
    # deck: nose -> belt at cs, belt -> tail at cs+cl, tail -> 0.94 tail at 1 (linear pieces)
    if t<=cs: deck=c["nose"]+(c["belt"]-c["nose"])*ss(t/cs)
    elif t<=cs+cl: deck=c["belt"]+(max(c["belt"],c["tail"])-c["belt"])*ss((t-cs)/cl)
    else: deck=max(c["belt"],c["tail"])+(c["tail"]*0.94-max(c["belt"],c["tail"]))*ss((t-cs-cl)/(1-cs-cl))
    cab=ss((t-cs)/(0.3*cl))*ss((cs+cl-t)/(0.4*cl))
    top=(deck+(1-deck)*cab)*H
    wf=0.8+0.2*ss(t/0.2) - 0.1*ss((t-0.8)/0.2)
    hw=W*wf/2
    ybd=RH+0.95*(top-RH)
    yb=ybd+(c["belt"]*H-ybd)*cab
    rw=hw*(0.6+(c["roof"]-0.6)*cab)
    ys=RH+0.35*(yb-RH)
    pts=[(-0.85*hw,RH),(0.85*hw,RH),(hw,ys),(0.97*hw,yb),(rw,top),(-rw,top),(-0.97*hw,yb),(-hw,ys)]
    return pts, top
for name,c in cars.items():
    xf=c["WB"]/2+c["FO"]
    faces=[];tops=[]
    for i in range(N):
        t=i/(N-1)
        pts,top=station(c,t); tops.append(top)
        pl=Plane(origin=(xf-t*c["L"],0,0),x_dir=(0,0,-1),z_dir=(1,0,0))
        w=Wire.make_polygon([pl.from_local_coords((x,y,0)) for x,y in pts])
        r=c["er"]*c["H"]
        w=w.fillet_2d(r,w.vertices())
        faces.append(Face(w))
    t0=time.time()
    body=loft(faces)
    bb=body.bounding_box()
    print("%-18s N=%d stated top %.0f built %.0f (+%.1f%%) half %.0f built %.0f  loft %.2fs vol %.3g valid %s"%(name,N,max(tops),bb.max.Y,100*(bb.max.Y/max(tops)-1),c["W"]/2,bb.max.Z,time.time()-t0,body.volume,body.is_valid),flush=True)
