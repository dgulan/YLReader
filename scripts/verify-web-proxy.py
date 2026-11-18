"""Check security headers/login throttling in a disposable offline nginx container."""
from pathlib import Path
import argparse,json,subprocess,uuid
p=argparse.ArgumentParser();p.add_argument('--image',required=True);a=p.parse_args()
root=Path(__file__).resolve().parents[1];ident=uuid.uuid4().hex[:12];name='ylreader-proxy-check-'+ident
out=root/'.toolchain'/name;out.mkdir();result={'passed':False}
def run(*args):return subprocess.run(['docker',*args],capture_output=True,timeout=60)
try:
 image=run('image','inspect',a.image,'--format','{{.Id}}')
 if image.returncode:raise RuntimeError('requires existing local nginx image')
 r=run('run','-d','--pull=never','--name',name,'--label','ylreader.proxy-check='+ident,'--network','none','--add-host','app:127.0.0.1','--mount',f'type=bind,source={root}/frontend/nginx.conf,target=/etc/nginx/conf.d/default.conf,readonly','--mount',f'type=bind,source={root}/frontend/dist,target=/usr/share/nginx/html,readonly','--entrypoint','nginx',image.stdout.decode().strip(),'-g','daemon off;')
 if r.returncode:raise RuntimeError('nginx test startup failed')
 r=run('exec',name,'wget','-S','-O','/dev/null','http://127.0.0.1/')
 headers=r.stderr.decode(errors='replace');(out/'headers.txt').write_text(headers,encoding='utf-8')
 result['headers_ok']=r.returncode==0 and all(k.lower() in headers.lower() for k in ['X-Content-Type-Options: nosniff','X-Frame-Options: DENY','Referrer-Policy: strict-origin-when-cross-origin','Content-Security-Policy:'])
 statuses=[]
 for _ in range(12):
  r=run('exec',name,'wget','-S','-O','-','--post-data={}','http://127.0.0.1/api/v1/auth/login')
  statuses.append('429' if '429' in r.stderr.decode(errors='replace') else 'upstream')
  # Busybox wget may discard HTTP error bodies; JSON shape is also verified
  # statically in the nginx return directive, while this checks actual status.
 result['login_429_count']=statuses.count('429');result['passed']=result['headers_ok'] and result['login_429_count']>0
finally:
 owner=run('inspect',name,'--format','{{index .Config.Labels "ylreader.proxy-check"}}')
 result['cleanup']=owner.returncode!=0 or (owner.stdout.decode().strip()==ident and run('rm','-f','-v',name).returncode==0)
 result['passed']=result['passed'] and result['cleanup']
 (out/'result.json').write_text(json.dumps(result,indent=2),encoding='utf-8');print(out/'result.json')
 if not result['passed']:raise RuntimeError('proxy baseline check failed')
