# Model of the Android nvram driver: the stock kernel driver of the UC-P10
# (Grandstream nvram, 1.0.0.94 Image; the driver code of 1.0.6.20 is the same).
#
# What it models: the driver's scan of the 32-part ring (magic, checksum with
# the signed bound, highest write counter, default part 0), the load of the
# active part (flags, zlib length, inflate into 0x1ffd8 bytes), the record
# parse with set and lock, and get("72005"). drv(ring) returns the set of
# values that get("72005") can give over every world that the bytes do not
# decide (a checksum or an inflate that reads past the part, a parse that runs
# into memory the driver does not clear).
#
# Where it came from: the independent Opus release review of the nvram
# reader (AUTOUPDATE-CHECK-2-REVIEW, round 1; the same file in round 2,
# AUTOUPDATE-HARDENING-R2-REVIEW). The reviewer wrote it from the
# disassembly of the driver, not from the Go code of internal/nvram. The
# reviewer's file had sha256 3ed5428508dd1bc1ae0e0a83d52e75000e6058914659f8e35c348745677088ad.
# Only this header is new (it replaces the first two comment lines). The code
# below is the reviewer's, unchanged: change it only with a new review.
#
# It holds no key, no secret and no vendor data. ci/nvram-model-diff.py runs it
# against the Go parser (ci/lint.sh).
# drv(ring) -> set of possible results of get("72005") over the nondeterministic worlds.
import struct, zlib, itertools
PART=0x10000; HDR=0x28; DATA=PART-HDR; OUTMAX=0x1ffd8
MISSING="<missing>"; NONDET="<nondet>"

def s32(x):
    x&=0xffffffff
    return x-0x100000000 if x&0x80000000 else x

def sumcheck(p,n):
    """scan/hdrcheck (0x7cc33c/0x7cc998): returns True/False, or None when the loop reads past the 64 KiB buffer."""
    m=s32(((n+1)&0xffffffff)&0xfffffffe)
    if m<=0: s=0
    elif m>DATA: return None
    else:
        s=sum(struct.unpack_from(">%dH"%(m//2),p,HDR))&0xffff
    want=(-s)&0xffff
    return struct.unpack_from("<H",p,0x12)[0]==((want>>8)|((want&0xff)<<8))

def scanpart(p):
    if p[:4]!=b"FLSH": return False
    return sumcheck(p,struct.unpack_from("<I",p,12)[0])

def load_text(p, accepted_in_world):
    """load (0x7cd1d0) of the chosen part: returns text bytes (inflated), b"" for an empty store, or NONDET."""
    if p[:4]!=b"FLSH": return None                     # Invalid nvram data: no store
    flags=struct.unpack_from("<H",p,0x10)[0]
    n=struct.unpack_from("<I",p,12)[0]
    if flags!=0:
        if not accepted_in_world: return None          # invalid chksum: no store
    else:
        n=0xffd8
    if n<=DATA: inp=p[HDR:HDR+n]; more=False
    else: inp=p[HDR:PART]; more=True                   # avail_in reaches past the buffer
    d=zlib.decompressobj(15)
    try:
        out=d.decompress(inp,OUTMAX+1)
    except zlib.error:
        return b""                                     # corrupt -> text cleared (all NUL)
    if d.eof and len(out)<=OUTMAX: return out
    if len(out)>OUTMAX or d.unconsumed_tail: return b""  # output full: not Z_STREAM_END
    if more: return NONDET                             # needs input bytes after the part
    return b""

def parse(text):
    """parse (0x7cd914) + set (0x7cd5bc) + lock (0x7cd7e0); returns table key->[value,flags] or NONDET."""
    if text is NONDET: return NONDET
    if text is None or text==b"": return {}            # empty store (cleared text area is all NUL)
    buf=bytearray(text)
    if len(buf)>=OUTMAX: buf[OUTMAX-2]=0; buf[OUTMAX-1]=0
    tab={}
    pos=0
    def at(i):
        if i>=len(buf): raise IndexError
        return buf[i]
    try:
        while True:
            if at(pos)==0: break
            j=pos
            while at(j) not in (0,0x3d): j+=1
            if at(j)==0: break                         # strchr(=) is NULL: stop
            eq=j; k=eq+1
            while at(k)!=0: k+=1
            name=bytes(buf[pos:eq]); value=bytes(buf[eq+1:k])
            a=name[0] if name else 0
            if a in (0x3a,1): key=name[1:]; w28=1; w25=0
            elif a in (2,3): key=name[1:]; w25=1; w28=0
            else: key=name; w25=0; w28=0
            t=tab.get(key); ok=True
            if t is not None and (t[1]&6):
                if not w25: ok=False
                elif a==2 and (t[1]&4): ok=False
            if ok:
                if t is not None and not w28: t[1]&=~1
                if t is None: t=[None,0]; tab[key]=t
                t[0]=value
                if w28: t[1]|=1
            if a in (2,3):
                t=tab.get(key)
                if t is None: tab[key]=[b"",2 if a==2 else 4]
                elif (t[1]&6)==0: t[1]|=(2 if a==2 else 4)
            pos=k+1
    except IndexError:
        return NONDET                                  # ran into uninitialised store memory
    return tab

def get72005(tab):
    if tab is NONDET: return NONDET
    t=tab.get(b"72005")
    if t is None or (t[1]&4): return MISSING
    return t[0].decode("latin-1")

def drv(ring, maxnd=6):
    parts=[ring[i*PART:(i+1)*PART] for i in range(32)]
    acc=[scanpart(p) for p in parts]
    nd=[i for i in range(32) if acc[i] is None]
    if len(nd)>maxnd: return {NONDET}
    res=set()
    for bits in itertools.product([False,True],repeat=len(nd)):
        a=list(acc)
        for i,b in zip(nd,bits): a[i]=b
        mx=0; act=0
        for i in range(32):
            if a[i]:
                c=struct.unpack_from("<Q",parts[i],0x18)[0]
                if c>=mx: mx=c; act=i
        res.add(get72005(parse(load_text(parts[act],a[act]))))
    return res
