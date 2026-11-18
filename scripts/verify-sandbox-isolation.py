"""Run offline sandbox probes in disposable, strictly memory-limited containers.

Build cmd/sandboxprobe for Linux first. Requires a trusted, already-local image;
never pulls, uses production configuration, or removes unowned containers.
"""
from pathlib import Path
import argparse,hashlib,json,subprocess,uuid
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--image', required=True, help='Trusted local Linux image; no pull is performed')
parser.add_argument('--binary', type=Path, help='Linux CGO-disabled sandboxprobe binary')
args=parser.parse_args()
root=Path(__file__).resolve().parents[1]
binary=(args.binary or root/'.toolchain/sandboxprobe-linux').resolve()
ident=uuid.uuid4().hex[:12];out=root/'.toolchain'/('sandbox-isolation-'+ident);out.mkdir()
result={'run_id':ident,'passed':False,'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'cases':{},'cleanup':{}}
def run(*args,data=None,allowed=False,timeout=30):
 p=subprocess.run(['docker',*args],input=data,capture_output=True,timeout=timeout)
 if p.returncode and not allowed:raise RuntimeError(p.stderr.decode(errors='replace')[-1000:])
 return p
image=run('image','inspect',args.image,'--format','{{.Id}}').stdout.decode().strip();result['image']=image
names=[]
try:
 for case,request,expected in [
  ('normal',{'script':"'normal'"},'normal'),
  ('structured',{'script':'({name:"book",items:[1,true,null]})','structured':True},'structured'),
  ('input-limit',{'script':'x'*(262144+1)},'rule_execution_failed'),
  ('cpu-timeout',{'script':'while(true){}'},'sandbox_timeout'),
  ('native-oom',{'script':'"x".repeat(268435456)'},'oom'),
  ('after-oom',{'script':"'recovered'"},'recovered')]:
  name='ylreader-sandbox-probe-'+ident+'-'+case;names.append(name)
  cmd=['run','-i','--pull=never','--name',name,'--label','ylreader.sandbox-probe='+ident,'--network','none','--read-only','--user','10001:10001','--cap-drop','ALL','--security-opt','no-new-privileges','--pids-limit','64','--memory','128m','--memory-swap','128m','--cpus','1','--mount',f'type=bind,source={binary},target=/probe,readonly','--entrypoint','/probe',image]
  p=run(*cmd,data=json.dumps(request).encode(),allowed=True,timeout=20)
  info=json.loads(run('inspect',name).stdout)[0]
  host=info['HostConfig'];assert host['Memory']==128*1024*1024 and host['MemorySwap']==host['Memory'] and host['NetworkMode']=='none' and not host.get('PortBindings')
  assert host['ReadonlyRootfs'] and host['PidsLimit']==64 and info['Config']['User']=='10001:10001'
  assert 'ALL' in host['CapDrop'] and 'no-new-privileges' in host['SecurityOpt']
  state=info['State'];result['cases'][case]={'exit_code':p.returncode,'oom_killed':state['OOMKilled']}
  (out/(case+'.stdout')).write_bytes(p.stdout);(out/(case+'.stderr')).write_bytes(p.stderr)
  if expected=='oom':assert state['OOMKilled'] and p.returncode==137,result['cases'][case]
  else:
   assert p.returncode==0,(case,p.returncode)
   reply=json.loads(p.stdout)
   if expected=='structured':assert reply['ok'] and reply['value']=={'name':'book','items':[1,True,None]}
   elif expected in ('rule_execution_failed','sandbox_timeout'):assert not reply['ok'] and reply['error']==expected
   else:assert reply['ok'] and reply['value']==expected
 result['passed']=True
finally:
 for name in names:
  check=run('inspect',name,'--format','{{index .Config.Labels "ylreader.sandbox-probe"}}',allowed=True)
  if check.returncode==0 and check.stdout.decode().strip()==ident:result['cleanup'][name]=run('rm','-f','-v',name,allowed=True).returncode==0
  else:result['cleanup'][name]=check.returncode!=0
 (out/'result.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8');print(out/'result.json',flush=True)
 if not all(result['cleanup'].values()):raise RuntimeError('owned probe cleanup failed')
