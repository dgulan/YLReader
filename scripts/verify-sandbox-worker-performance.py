"""Offline synthetic worker measurements. No pull, deployment or production data."""
from pathlib import Path
import argparse, json, subprocess, time, uuid

p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--image',required=True,help='Trusted local utility image with sh/cat')
p.add_argument('--worker-image',required=True,help='Already-built amd64 final worker image')
p.add_argument('--binary',type=Path,required=True,help='Linux amd64 sandbox integration test executable')
args=p.parse_args()
root=Path(__file__).resolve().parents[1]
ident=uuid.uuid4().hex[:12];label='ylreader.worker-performance='+ident
out=root/'.toolchain'/('worker-performance-'+ident);out.mkdir()
result={'run_id':ident,'passed':False,'cleanup':{},'runs':[]}
containers=[];volumes=[]

def run(*a,allowed=False,timeout=300):
 r=subprocess.run(['docker',*a],capture_output=True,timeout=timeout)
 if r.returncode and not allowed:raise RuntimeError(r.stderr.decode(errors='replace')[-1500:])
 return r

def snapshot(names):
 rows=[]
 for name in names:
  j=json.loads(run('inspect',name).stdout)[0]
  # A separate, unprivileged inspector only reads the worker's cgroup mount.
  inspector='ylreader-perf-reader-'+uuid.uuid4().hex[:12];containers.append(inspector)
  peak=run('run','--rm','--pull=never','--name',inspector,'--label',label,'--pid','container:'+name,'--network','none','--read-only','--user','10001:10001','--cap-drop','ALL','--security-opt','no-new-privileges','--entrypoint','cat',utility,'/proc/1/root/sys/fs/cgroup/memory.peak',allowed=True)
  rows.append({'name':name,'restart_count':j['RestartCount'],'running':j['State']['Running'],'memory_peak_bytes':int(peak.stdout) if peak.returncode==0 and peak.stdout.strip().isdigit() else None,'peak_read_error':peak.stderr.decode(errors='replace')[-300:] if peak.returncode else ''})
 return rows

try:
 utility=run('image','inspect',args.image,'--format','{{.Id}}').stdout.decode().strip()
 wi=json.loads(run('image','inspect',args.worker_image).stdout)[0]
 if wi['Architecture']!='amd64' or wi['Os']!='linux':raise RuntimeError('requires native amd64 worker image')
 worker_image=wi['Id'];result['worker_image']=worker_image;result['utility_image']=utility
 result['engine']=json.loads(run('info','--format','{{json .}}').stdout)
 # Retain only reproducibility metadata, not daemon environment/configuration.
 result['engine']={k:result['engine'].get(k) for k in ['Architecture','NCPU','MemTotal','ServerVersion','KernelVersion','OperatingSystem']}
 if result['engine']['Architecture'] not in ['x86_64','amd64']:raise RuntimeError('native amd64 host required')
 import hashlib
 binary=args.binary.resolve();result['test_binary_sha256']=hashlib.sha256(binary.read_bytes()).hexdigest()
 for repeat in [1,2]:
  for count in [0,1,4]:
   prefix=f'ylreader-perf-{ident}-{repeat}-{count}';names=[];mounts=[];paths=[]
   since=run('info','--format','{{.SystemTime}}').stdout.decode().strip()
   for i in range(count):
    volume=prefix+'-ipc-'+str(i);volumes.append(volume)
    run('volume','create','--label',label,volume)
    init=prefix+'-init-'+str(i);containers.append(init)
    run('run','--rm','--pull=never','--name',init,'--label',label,'--network','none','--user','0:0','--mount',f'type=volume,source={volume},target=/ipc','--entrypoint','sh',utility,'-c','chown 10001:10001 /ipc && chmod 0700 /ipc')
    name=prefix+'-worker-'+str(i);containers.append(name);names.append(name)
    run('run','-d','--pull=never','--name',name,'--label',label,'--network','none','--read-only','--user','10001:10001','--cap-drop','ALL','--security-opt','no-new-privileges','--memory','128m','--memory-swap','128m','--cpus','1','--pids-limit','64','--restart','on-failure:3','--mount',f'type=volume,source={volume},target=/ipc',worker_image)
    mounts+=['--mount',f'type=volume,source={volume},target=/ipc-{i}'];paths.append(f'/ipc-{i}/worker.sock')
   time.sleep(1)
   phases=[('normal',False,False)]
   if count:phases+= [('edge',True,False),('admission',False,True)]
   for phase,edges,admission in phases:
    if admission:
     # Restart cold workers: the edge phase deliberately drives one worker
     # into OOM, and admission must measure the budget, not a warm heap.
     for name in names:run('restart','-t','2',name)
     time.sleep(1)
    parent=prefix+'-'+phase;containers.append(parent)
    command=['run','--rm','--pull=never','--name',parent,'--label',label,'--network','none','--read-only','--user','10001:10001','--cap-drop','ALL','--security-opt','no-new-privileges','--memory','512m','--memory-swap','512m','--cpus','1','--pids-limit','64',*mounts,'--mount',f'type=bind,source={binary},target=/bench,readonly','-e','TEST_WORKER_PERFORMANCE='+('remote' if count else 'local'),'-e','TEST_WORKER_SOCKETS='+','.join(paths),'-e','TEST_WORKER_EDGES='+('1' if edges else '0'),'-e','TEST_WORKER_ADMISSION='+('1' if admission else '0'),'--entrypoint','/bench',utility,'-test.run=^TestWorkerPerformance$','-test.v','-test.timeout=6m']
    r=run(*command,allowed=True,timeout=420)
    stem=f'{repeat}-{count}-'+phase
    (out/(stem+'.stdout')).write_bytes(r.stdout);(out/(stem+'.stderr')).write_bytes(r.stderr)
    rows=[]
    for line in r.stdout.decode(errors='replace').splitlines():
     if line.startswith(('WORKER_PERF ','WORKER_EDGE ','WORKER_ADMISSION ')):rows.append(json.loads(line.split(' ',1)[1]))
    entry={'repeat':repeat,'workers':count,'phase':phase,'edges':edges,'admission':admission,'exit_code':r.returncode,'metrics':rows,'worker_resources':snapshot(names)};result['runs'].append(entry)
    until=run('info','--format','{{.SystemTime}}').stdout.decode().strip()
    events=run('events','--since',since,'--until',until,'--filter','label='+label,'--filter','event=oom','--format','{{json .}}',allowed=True)
    (out/(stem+'.oom-events.jsonl')).write_bytes(events.stdout)
    entry['oom_events_since_profile_start']=len(events.stdout.splitlines())
    if r.returncode:raise RuntimeError('performance test failed: '+stem)
    if phase=='normal' and (len(rows)!=18 or any(x['restart_count'] for x in entry['worker_resources'])):raise RuntimeError('incomplete normal phase or unexpected restart')
    if phase=='edge' and len(rows)!=5:raise RuntimeError('incomplete edge phase')
    if phase=='admission' and len(rows)!=3:raise RuntimeError('incomplete admission phase')
   for name in names:
    run('stop','--time','2',name)
 result['passed']=True
finally:
 for kind,names in [('container',containers),('volume',volumes)]:
  for name in names:
   fmt='{{index .Config.Labels "ylreader.worker-performance"}}' if kind=='container' else '{{index .Labels "ylreader.worker-performance"}}'
   inspect=run('inspect',name,'--format',fmt,allowed=True) if kind=='container' else run('volume','inspect',name,'--format',fmt,allowed=True)
   if inspect.returncode:ok=True
   elif inspect.stdout.decode().strip()==ident:ok=run(*(['rm','-f','-v',name] if kind=='container' else ['volume','rm',name]),allowed=True).returncode==0
   else:ok=False
   result['cleanup'][name]=ok
 result['passed']=result['passed'] and all(result['cleanup'].values())
 (out/'result.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8');print(out/'result.json',flush=True)
 if not all(result['cleanup'].values()):raise RuntimeError('owned object cleanup failed')
