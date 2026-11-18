"""Run the bounded safe-Fetcher diagnostic in a disposable Linux container.
Input contains at most three explicitly chosen public GET targets; no source JS.
"""
from pathlib import Path
import argparse, hashlib, json, subprocess, uuid

p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--binary',type=Path,required=True)
p.add_argument('--input',type=Path,required=True)
p.add_argument('--image',default='yreader-pre-release:race')
a=p.parse_args()
root=Path(__file__).resolve().parents[1];ident=uuid.uuid4().hex[:12]
out=root/'.toolchain'/('source-network-'+ident);out.mkdir()
name='ylreader-source-network-'+ident;label='ylreader.source-network='+ident
result={'passed':False,'cleanup':False,'purpose':'network_only_not_source_reading'}

def docker(*args,data=None,allowed=False):
 r=subprocess.run(['docker',*args],input=data,capture_output=True,timeout=75)
 if r.returncode and not allowed:raise RuntimeError('docker operation failed')
 return r

try:
 data=a.input.read_bytes()
 if len(data)>64<<10:raise RuntimeError('input exceeds limit')
 binary=a.binary.resolve()
 result['binary_sha256']=hashlib.sha256(binary.read_bytes()).hexdigest()
 result['input_sha256']=hashlib.sha256(data).hexdigest()
 info=json.loads(docker('image','inspect',a.image).stdout)[0]
 if info['Os']!='linux' or info['Architecture']!='amd64':raise RuntimeError('native linux/amd64 runtime required')
 result['runtime_image']=info['Id']
 command=['run','--rm','--pull=never','-i','--name',name,'--label',label,'--network','bridge','--read-only','--user','10001:10001','--cap-drop','ALL','--security-opt','no-new-privileges','--memory','256m','--memory-swap','256m','--cpus','1','--pids-limit','64','--mount',f'type=bind,source={binary},target=/diagnose,readonly','--entrypoint','/diagnose',info['Id']]
 r=docker(*command,data=data,allowed=True)
 (out/'diagnostics.jsonl').write_bytes(r.stdout)
 (out/'stderr.txt').write_bytes(r.stderr)
 result['exit_code']=r.returncode
 rows=[json.loads(s) for s in r.stdout.decode().splitlines()]
 result['outcomes']=[{'label':row.get('label'),'outcome':row.get('outcome'),'http_status':row.get('http_status')} for row in rows]
 result['passed']=r.returncode==0 and bool(rows) and all(row.get('outcome') for row in rows)
finally:
 inspected=docker('inspect',name,allowed=True)
 if inspected.returncode:result['cleanup']=True
 else:
  info=json.loads(inspected.stdout)[0]
  if info['Config'].get('Labels',{}).get('ylreader.source-network')==ident:
   result['cleanup']=docker('rm','-f',name,allowed=True).returncode==0
 result['passed']=result['passed'] and result['cleanup']
 (out/'result.json').write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8')
 print(out/'result.json',flush=True)
 if not result['cleanup']:raise RuntimeError('owned diagnostic container cleanup failed')
if not result['passed']:raise SystemExit(1)
