"""Validate worker IPC in owned, constrained, offline containers.
Requires a trusted already-local Go test image and a Linux worker binary.
Does not pull, deploy, use production configuration or remove unowned objects.
"""
from pathlib import Path
import argparse,datetime,hashlib,json,subprocess,time,uuid
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--image',required=True,help='Trusted local Go test image')
parser.add_argument('--binary',type=Path,help='Linux CGO-disabled worker binary')
parser.add_argument('--worker-image',help='Already-local final worker image')
parser.add_argument('--platform',choices=['linux/amd64','linux/arm64'])
args=parser.parse_args()
if args.worker_image and args.binary:parser.error('--worker-image and --binary are mutually exclusive')
root=Path(__file__).resolve().parents[1];ident=uuid.uuid4().hex[:12]
worker='ylreader-worker-'+ident;parent='ylreader-worker-parent-'+ident;init='ylreader-worker-init-'+ident;bad='ylreader-worker-rejected-'+ident;volume='ylreader-worker-ipc-'+ident
out=root/'.toolchain'/('sandbox-worker-container-'+ident);out.mkdir()
result={'run_id':ident,'passed':False,'cleanup':{}}
def run(*args,data=None,allowed=False,timeout=300):
 p=subprocess.run(['docker',*args],input=data,capture_output=True,timeout=timeout)
 if p.returncode and not allowed:raise RuntimeError(p.stderr.decode(errors='replace')[-1200:])
 return p
try:
 image=run('image','inspect',args.image,'--format','{{.Id}}').stdout.decode().strip();result['image']=image
 worker_image=image;worker_mount=[]
 if args.worker_image:
  worker_image=run('image','inspect',args.worker_image,'--format','{{.Id}}').stdout.decode().strip();result['worker_image']=worker_image
  wi=json.loads(run('image','inspect',worker_image).stdout)[0]
  result['worker_config']={k:wi['Config'].get(k) for k in ['User','Entrypoint','Cmd','Volumes','ExposedPorts']}
  result['worker_architecture']=wi['Architecture']
  if wi['Os']!='linux' or (args.platform and wi['Architecture']!=args.platform.split('/')[1]):raise RuntimeError('worker platform mismatch')
  if wi['Config'].get('User')!='10001:10001' or wi['Config'].get('Entrypoint')!=['/worker'] or wi['Config'].get('Cmd')!=['-socket','/ipc/worker.sock'] or wi['Config'].get('Volumes') or wi['Config'].get('ExposedPorts'):raise RuntimeError('worker image configuration mismatch')
 else:
  binary=(args.binary or root/'.toolchain/sandboxworker-linux').resolve();result['binary_sha256']=hashlib.sha256(binary.read_bytes()).hexdigest()
  worker_mount=['--mount',f'type=bind,source={binary},target=/worker,readonly']
 platform=['--platform',args.platform] if args.platform else []
 result['platform']=args.platform
 run('volume','create','--label','ylreader.worker-integration='+ident,volume)
 run('run','--rm','--pull=never','--name',init,'--label','ylreader.worker-integration='+ident,'--network','none','--user','0:0','--mount',f'type=volume,source={volume},target=/ipc','--entrypoint','sh',image,'-c','chown 10001:10001 /ipc && chmod 0700 /ipc')
 rejected=run('run','--pull=never','--name',bad,'--label','ylreader.worker-integration='+ident,'--network','none','--user','10001:10001','--cap-drop','ALL','--security-opt','no-new-privileges','--pids-limit','64','--memory','128m','--memory-swap','128m','--cpus','1',*platform,*worker_mount,'-e','TZ=Asia/Shanghai','--mount',f'type=volume,source={volume},target=/ipc','--entrypoint','/worker',worker_image,'-socket','/ipc/worker.sock',allowed=True)
 result['unconstrained_root_rejected']=rejected.returncode==1
 (out/'rejected.stderr').write_bytes(rejected.stderr)
 if not result['unconstrained_root_rejected']:raise RuntimeError('writable-root profile was not rejected')
 # Event timestamps belong to the daemon; the desktop VM clock can differ.
 since=run('info','--format','{{.SystemTime}}').stdout.decode().strip()
 run('run','-d','--pull=never','--name',worker,'--label','ylreader.worker-integration='+ident,'--restart','on-failure:3','--network','none','--read-only','--user','10001:10001','--cap-drop','ALL','--security-opt','no-new-privileges','--pids-limit','64','--memory','128m','--memory-swap','128m','--cpus','1',*platform,*worker_mount,'-e','TZ=Asia/Shanghai','--mount',f'type=volume,source={volume},target=/ipc','--entrypoint','/worker',worker_image,'-socket','/ipc/worker.sock')
 time.sleep(2)
 command=['run','--rm','--pull=never','--name',parent,'--label','ylreader.worker-integration='+ident,'--network','none','--read-only','--user','10001:10001','--tmpfs','/tmp:rw,exec,mode=1777','-e','TEST_SANDBOX_WORKER_SOCKET=/ipc/worker.sock','-e','TEST_SANDBOX_WORKER_TZ=Asia/Shanghai','-e','GOCACHE=/cache','-e','GOMODCACHE=/modcache','-e','GOPROXY=off','-e','GOTOOLCHAIN=local','--mount',f'type=volume,source={volume},target=/ipc','--mount',f'type=bind,source={root}/backend,target=/src,readonly','--mount',f'type=bind,source={root}/.toolchain/gopath/pkg/mod,target=/modcache,readonly','--mount',f'type=bind,source={root}/.toolchain/backup-linux-gocache,target=/cache','--workdir','/src','--entrypoint','go',image,'test','-mod=readonly','-tags','integration','-race','-p','1','-json','./internal/engine/sandbox','./internal/engine/parser','-run','^TestWorker(ContainerIntegration|ParserIntegration)$','-count=1','-timeout=90s']
 p=run(*command,allowed=True);(out/'tests.jsonl').write_bytes(p.stdout);(out/'stderr.txt').write_bytes(p.stderr)
 info=json.loads(run('inspect',worker).stdout)[0];result['restart_count']=info['RestartCount'];result['parent_exit']=p.returncode
 (out/'worker.log').write_bytes(run('logs',worker,allowed=True).stderr)
 until=run('info','--format','{{.SystemTime}}').stdout.decode().strip()
 events=run('events','--since',since,'--until',until,'--filter','container='+worker,'--filter','event=oom','--format','{{json .}}',allowed=True)
 (out/'oom-events.jsonl').write_bytes(events.stdout);result['oom_events']=len(events.stdout.splitlines())
 result['passed']=p.returncode==0 and info['RestartCount']>=1 and result['oom_events']>=1
 if not result['passed']:raise RuntimeError('worker container integration failed; inspect results')
finally:
 for name in [parent,worker,init,bad]:
  label=run('inspect',name,'--format','{{index .Config.Labels "ylreader.worker-integration"}}',allowed=True)
  if label.returncode==0 and label.stdout.decode().strip()==ident:result['cleanup'][name]=run('rm','-f','-v',name,allowed=True).returncode==0
  else:result['cleanup'][name]=label.returncode!=0
 label=run('volume','inspect',volume,'--format','{{index .Labels "ylreader.worker-integration"}}',allowed=True)
 result['cleanup'][volume]=label.returncode!=0 or (label.stdout.decode().strip()==ident and run('volume','rm',volume,allowed=True).returncode==0)
 (out/'result.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8');print(out/'result.json',flush=True)
 if not all(result['cleanup'].values()):raise RuntimeError('owned container/volume cleanup failed')
