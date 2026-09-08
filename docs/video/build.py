#!/usr/bin/env python3
"""Build one video: opener (cartoon) + terminal segments rendered from casts by agg,
with a caption strip burned in from cue points. Everything real; only speed and
idle trimming are applied, and the first card says so."""
import json, os, subprocess, sys, tempfile
V=os.environ.get('VIDEO_DIR', os.path.dirname(os.path.abspath(__file__)))
AGG=os.environ.get('AGG', os.path.expanduser('~/.cargo/bin/agg'))

def cast_duration(cast):
    last=0.0
    with open(cast) as f:
        next(f)
        for line in f:
            try: last=float(json.loads(line)[0])
            except Exception: pass
    return last

def find_cue(cast, needle, after=0.0):
    """Time (in the cast's original clock) of the first output line containing needle."""
    with open(cast) as f:
        next(f)
        for line in f:
            try: t,_,s=json.loads(line)
            except Exception: continue
            if t>=after and needle in s: return t
    return None

def adjusted(cast, t, speed, idle):
    """Map an original time to the rendered timeline: idle gaps capped at `idle`, then /speed."""
    out=0.0; prev=0.0
    with open(cast) as f:
        next(f)
        for line in f:
            try: ts=float(json.loads(line)[0])
            except Exception: continue
            if ts>t: break
            out+=min(ts-prev, idle); prev=ts
    out+=min(t-prev, idle)
    return out/speed

def render(cast, speed, idle, out_mp4, font=20):
    gif=out_mp4.replace('.mp4','.gif')
    subprocess.run([AGG,'--speed',str(speed),'--idle-time-limit',str(idle),'--font-size',str(font),'--theme','monokai',cast,gif],check=True)
    subprocess.run(['ffmpeg','-hide_banner','-loglevel','error','-y','-i',gif,
        '-vf','scale=1920:960:force_original_aspect_ratio=decrease:flags=lanczos,pad=1920:960:(ow-iw)/2:(oh-ih)/2:color=#272822,pad=1920:1080:0:0:color=#141416,format=yuv420p','-r','30','-c:v','libx264','-pix_fmt','yuv420p',out_mp4],check=True)
    os.remove(gif)

def srt(cards, path):
    def ts(s):
        h=int(s//3600); m=int(s%3600//60); sec=s%60
        return f"{h:02d}:{m:02d}:{sec:06.3f}".replace('.',',')
    with open(path,'w') as f:
        for i,(a,b,text) in enumerate(cards,1):
            f.write(f"{i}\n{ts(a)} --> {ts(b)}\n{text}\n\n")

def concat_with_captions(segments, cards, out):
    """segments: list of mp4 paths in order; cards: (start,end,text) on the concatenated timeline."""
    lst=os.path.join(V,'concat.txt')
    with open(lst,'w') as f:
        for s in segments: f.write(f"file '{s}'\n")
    joined=os.path.join(V,'joined.mp4')
    subprocess.run(['ffmpeg','-hide_banner','-loglevel','error','-y','-f','concat','-safe','0','-i',lst,'-c','copy',joined],check=True)
    sub=os.path.join(V,'captions.srt'); srt(cards, sub)
    style="FontName=Liberation Sans,FontSize=9,PrimaryColour=&H00F2F2F2,OutlineColour=&H00141416,BackColour=&H00141416,BorderStyle=1,Outline=1,Shadow=0,Alignment=2,MarginV=7,MarginL=16,MarginR=16"
    subprocess.run(['ffmpeg','-hide_banner','-loglevel','error','-y','-i',joined,'-vf',f"subtitles={sub}:force_style='{style}'",'-c:v','libx264','-pix_fmt','yuv420p','-crf','20','-movflags','+faststart',out],check=True)
    return out

def duration(mp4):
    return float(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration','-of','csv=p=0',mp4]).decode().strip())


def split_cast(cast, at, a_out, b_out):
    """Split a cast at time `at` (original clock): events before → a_out, events from `at` → b_out re-based to 0."""
    with open(cast) as f:
        header=next(f); rest=[json.loads(l) for l in f if l.strip()]
    with open(a_out,'w') as f:
        f.write(header)
        for t,k,d in rest:
            if t<at: f.write(json.dumps([t,k,d])+"\n")
    with open(b_out,'w') as f:
        f.write(header)
        for t,k,d in rest:
            if t>=at: f.write(json.dumps([round(t-at,6),k,d])+"\n")

def no_stack(cards, gap=2.2):
    """Captions never overlap: each starts no earlier than the previous start + gap, and ends where the next begins."""
    cards=sorted(cards, key=lambda c:c[0]); out=[]
    for i,(a,b,t) in enumerate(cards):
        if out and a < out[-1][0]+gap: a=out[-1][0]+gap
        out.append([a,b,t])
    for i in range(len(out)-1):
        out[i][1]=max(out[i][0]+1.0, out[i+1][0]-0.1)
    return [tuple(c) for c in out]


def retime_burst(cast_in, cast_out, per_line=0.1, lead=0.4):
    """A cast whose output arrived all at once, re-timed one line at a time: the same bytes,
    in the same order, one event per line every `per_line` seconds. Pacing only."""
    with open(cast_in) as f:
        header=next(f); events=[json.loads(l) for l in f if l.strip()]
    text=''.join(d for _,k,d in events if k=='o')
    lines=text.split('\r\n')
    with open(cast_out,'w') as f:
        f.write(header)
        t=lead
        for i,line in enumerate(lines):
            chunk=line+('\r\n' if i<len(lines)-1 else '')
            f.write(json.dumps([round(t,3),'o',chunk])+"\n")
            t+=per_line
