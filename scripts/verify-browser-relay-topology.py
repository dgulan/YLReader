"""Offline two-container TCP/Unix/CONNECT/TLS topology fixture. No real solver."""
from pathlib import Path
import argparse,hashlib,json,subprocess,time,uuid
p=argparse.ArgumentParser();p.add_argument('--image',required=True);p.add_argument('--binary',type=Path,required=True);a=p.parse_args()
root=Path(__file__).resolve().parents[1];ident=uuid.uuid4().hex[:12]
prefix='ylreader-relay-'+ident;proxy=prefix+'-proxy';client=prefix+'-client';init=prefix+'-init';volume=prefix+'-ipc';label='ylreader.relay-topology='+ident
out=root/'.toolchain'/('browser-relay-topology-'+ident);out.mkdir()
result={'passed':False,'cleanup':{}}
def run(*args,allowed=False,timeout=120):
 r=subprocess.run(['docker',*args],capture_output=True,timeout=timeout)
 if r.returncode and not allowed:raise RuntimeError(r.stderr.decode(errors='replace')[-800:])
 return r
try:
 image=run('image','inspect',a.image,'--format','{{.Id}}').stdout.decode().strip();binary=a.binary.resolve()
 result.update(image=image,binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest())
 run('volume','create','--label',label,volume)
 run('run','--rm','--pull=never','--name',init,'--label',label,'--network','none','--user','0:0','--mount',f'type=volume,source={volume},target=/ipc','--entrypoint','sh',image,'-c','chown 10001:10001 /ipc && chmod 0700 /ipc')
 common=['--pull=never','--label',label,'--network','none','--read-only','--user','10001:10001','--cap-drop','ALL','--security-opt','no-new-privileges','--memory','256m','--memory-swap','256m','--cpus','1','--pids-limit','64','--tmpfs','/tmp:rw,mode=1777','--mount',f'type=volume,source={volume},target=/ipc','--mount',f'type=bind,source={binary},target=/tests,readonly']
 run('run','-d','--name',proxy,*common,'-e','TEST_RELAY_TOPOLOGY_ROLE=proxy','--entrypoint','/tests',image,'-test.run=^TestRelayTopologyHelper$','-test.v','-test.timeout=50s')
 info=json.loads(run('inspect',proxy).stdout)[0]
 result['proxy_network']=info['HostConfig']['NetworkMode']
 if result['proxy_network']!='none' or info['HostConfig'].get('PortBindings'):raise RuntimeError('unsafe fixture topology')
 run('create','--name',client,*common,'-e','TEST_RELAY_TOPOLOGY_ROLE=client','--entrypoint','/tests',image,'-test.run=^TestRelayTopologyHelper$','-test.v','-test.timeout=30s')
 ci=json.loads(run('inspect',client).stdout)[0];result['client_network']=ci['HostConfig']['NetworkMode']
 if result['client_network']!='none' or ci['HostConfig'].get('PortBindings'):raise RuntimeError('unsafe client topology')
 r=run('start','-a',client,allowed=True,timeout=45);(out/'client.stdout').write_bytes(r.stdout);(out/'client.stderr').write_bytes(r.stderr)
 ci=json.loads(run('inspect',client).stdout)[0];result['client_exit']=ci['State']['ExitCode']
 waited=run('wait',proxy,timeout=55);result['proxy_exit']=int(waited.stdout.strip())
 logs=run('logs',proxy,allowed=True);(out/'proxy.stdout').write_bytes(logs.stdout);(out/'proxy.stderr').write_bytes(logs.stderr)
 result['passed']=result['client_exit']==0 and result['proxy_exit']==0
finally:
 for name in [client,proxy,init]:
  owner=run('inspect',name,'--format','{{index .Config.Labels "ylreader.relay-topology"}}',allowed=True)
  result['cleanup'][name]=owner.returncode!=0 or (owner.stdout.decode().strip()==ident and run('rm','-f','-v',name,allowed=True).returncode==0)
 owner=run('volume','inspect',volume,'--format','{{index .Labels "ylreader.relay-topology"}}',allowed=True)
 result['cleanup'][volume]=owner.returncode!=0 or (owner.stdout.decode().strip()==ident and run('volume','rm',volume,allowed=True).returncode==0)
 result['passed']=result['passed'] and all(result['cleanup'].values())
 (out/'result.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8');print(out/'result.json')
 if not result['passed']:raise RuntimeError('relay topology failed; inspect records')
