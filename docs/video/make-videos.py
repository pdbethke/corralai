#!/usr/bin/env python3
"""Casts in, two MP4s out. Re-runnable: python3 make-videos.py
Inputs: opener.mp4 (the cartoon), certify.cast, review.cast, record.cast, closing.cast.
Every segment is one real run at 1x; idle waits are capped at IDLE seconds; the last frame
of each segment is held so the output can be read. The first card says so."""
import sys, os, subprocess
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from build import *
IDLE=6.0; SPEED=1

IDLES={}
def seg(name, hold, speed=None, idle=None):
    cast=f'{V}/{name}.cast'; raw=f'{V}/seg-{name}-raw.mp4'; out=f'{V}/seg-{name}.mp4'
    if idle: IDLES[name]=idle
    render(cast, speed or SPEED, idle or IDLE, raw)
    subprocess.run(['ffmpeg','-hide_banner','-loglevel','error','-y','-i',raw,'-vf',f'tpad=stop_mode=clone:stop_duration={hold}','-c:v','libx264','-pix_fmt','yuv420p',out],check=True)
    os.remove(raw)
    return out

SPEEDS={}
def cue(name, needle, after=0.0):
    c=f'{V}/{name}.cast'; t=find_cue(c, needle, after)
    return None if t is None else adjusted(c, t, SPEEDS.get(name, SPEED), IDLES.get(name, IDLE))

cartoon=os.path.join(os.path.dirname(V), 'design', 'corral-cartoon.jpeg')
if not os.path.exists(f'{V}/opener.mp4') and os.path.exists(cartoon):
    subprocess.run(['ffmpeg','-hide_banner','-loglevel','error','-y','-loop','1','-i',cartoon,'-t','3.5','-vf',
        'scale=1920:960:force_original_aspect_ratio=decrease,pad=1920:960:(ow-iw)/2:(oh-ih)/2:color=#141416,pad=1920:1080:0:0:color=#141416,format=yuv420p',
        '-r','30','-c:v','libx264','-pix_fmt','yuv420p',f'{V}/opener.mp4'],check=True)
OP=duration(f'{V}/opener.mp4')
CLOSING=[  # cards on the closing segment, relative to its start
    (0.0, 'the record — corral\'s own, on a branch of its repository: a fresh clone, then verify'),
    ('chain intact', 'every entry carries the hash of the one before it — edit one, its signature breaks; remove one, the next link breaks'),
    ('SELECT', 'DuckDB reads the branch in place, as tables — no database to run'),
    ('pushed 0 scan', 'the same entries pushed to MotherDuck: the same view, shared, scripts withheld'),
    ('MODEL', 'the seats, graded by the record they leave — a claim that did not reproduce is on the record too'),
]

def cards_for(segments, plan):
    """plan: list of (segment name, [(cue-needle-or-seconds, text), ...]); returns absolute (start,end,text)."""
    out=[]; offset=OP
    starts={}
    for name,cards in plan:
        d=duration(f'{V}/seg-{name}.mp4')
        times=[]
        for at,text in cards:
            t = at if isinstance(at,(int,float)) else cue(name, at)
            if t is None: print(f'  (no cue for {at!r} in {name}; skipped)'); continue
            times.append((offset+min(t,d-1.0), text))
        for i,(t,text) in enumerate(times):
            end = times[i+1][0] if i+1<len(times) else offset+d
            out.append((t, max(t+1.5, end-0.1), text))
        offset+=d
    return no_stack(out)

# ---- certify: the adversarial test
c_seg=seg('certify', 5.0, idle=3.5); cl=seg('closing', 4.0)
cards=[(0.0,'one real run on flask, 1× speed — each wait on the jail or a model trimmed to a few seconds, nothing else cut'),
       ('goal','the goals: what this file promises, derived from the code'),
       ('budgeted by complexity','forty faults planted in app.py — each one violates a goal'),
       ('killed 25 of 40','flask\'s own suite runs against every fault, in a jail: 25 killed, 15 survived — measured by execution, never a model\'s word'),
       ('survivor writer','a second model writes a test for each survivor; each test is proven alone against its fault'),
       ('kill rate 0.62 (','kill rate 0.62 · 14 of 15 gaps proven catchable — the verdict, signed'),
       ('ledger: entry written','one entry, on a branch in the repo'),
       ('chain intact','the chain, verified')]
out=concat_with_captions([f'{V}/opener.mp4', c_seg, cl], [(0,OP-0.1,'the architect, the builder, the inspector, the signed checklist — all the same model')]+cards_for([], [('certify',cards),('closing',CLOSING)]), f'{V}/corral-certify.mp4')
print('certify video:', out, round(duration(out)),'s')

# ---- review: the adversary
# the burst: the first output after the seats' long silence
import json as _j
burst=next(float(_j.loads(l)[0]) for i,l in enumerate(open(f'{V}/review.cast')) if i>0 and float(_j.loads(l)[0])>60)
split_cast(f'{V}/review.cast', burst-0.2, f'{V}/review-wait.cast', f'{V}/review-burst.cast')
retime_burst(f'{V}/review-burst.cast', f'{V}/review-out.cast', per_line=0.11)
rw=seg('review-wait', 0.5); r_seg=seg('review-out', 7.0); rec=seg('record', 8.0)
wcards=[(0.0,'one real run on flask; the five-minute wait for the two seats is trimmed, and the output is paced a line at a time so it can be read'),
        ('reviewer: claude-code','a coding agent that has never seen this repo is told the code is wrong and must hand back a script for every claim; a second agent will try to refute it')]
rcards=[('ID  TIER','five claims, two declared reproduced'),
        ('R1 — script','the reviewer\'s script for claim 1 — corral runs it, not the reviewer'),
        ('DEMOTED','corral ran the scripts: these two did not demonstrate their claim — demoted, on the record'),
        ('verifier codex','the verifier could not refute them: five claims stand as read, none by execution'),
        ('checked and found sound','what the verifier checked and found sound is on the record too'),
        ('ledger: review entry','the review is an entry in the same ledger as the audit')]
reccards=[(0.0,'a person checks the top claim by hand — one line, no error — and rules; the ruling is an entry too'),
          ('chain intact','audit, review, ruling: one chain'),
          ('brief —','the auditor\'s report: what the record says is open on these files, for whoever writes next'),
          ('SELECT','DuckDB over the branch: one scan, one review, one ruling')]
out=concat_with_captions([f'{V}/opener.mp4', rw, r_seg, rec, cl], [(0,OP-0.1,'the architect, the builder, the inspector, the signed checklist — all the same model')]+cards_for([], [('review-wait',wcards),('review-out',rcards),('record',reccards),('closing',CLOSING)]), f'{V}/corral-review.mp4')
print('review video:', out, round(duration(out)),'s')
