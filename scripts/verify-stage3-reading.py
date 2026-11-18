"""Windows local acceptance: owned temporary databases, original EPUB, real API/browser.
Requires a freshly built Windows server executable and already-local postgres/redis images.
No deployment, pull, production data or saved credentials.
"""
from pathlib import Path
import argparse,base64,json,os,secrets,socket,subprocess,time,urllib.request,urllib.error,urllib.parse,uuid,zipfile,hashlib
p=argparse.ArgumentParser();p.add_argument('--server',type=Path,required=True);p.add_argument('--reading-only',action='store_true');p.add_argument('--numbered-only',action='store_true');p.add_argument('--production-preview',action='store_true');p.add_argument('--sources-dir',type=Path);p.add_argument('--sources-only',action='store_true');p.add_argument('--reading-seconds',type=int,default=60);a=p.parse_args()
if not 60 <= a.reading_seconds <= 600:p.error('reading-seconds must be between 60 and 600')
root=Path(__file__).resolve().parents[1];ident=uuid.uuid4().hex[:10]
out=root/'.toolchain'/('stage3-reading-'+ident);out.mkdir();label='ylreader.stage3='+ident
names=['ylreader-stage3-pg-'+ident,'ylreader-stage3-redis-'+ident]
result={'passed':False,'cleanup':{}};server=None;log=None
result.update(server_sha256=hashlib.sha256(a.server.read_bytes()).hexdigest(),reading_seconds=a.reading_seconds,browser=os.environ.get('E2E_BROWSER','chromium'),physical_device_tested=False)
result['frontend_mode']='production_preview' if a.production_preview else 'development'
def docker(*args,data=None,allowed=False):
 r=subprocess.run(['docker',*args],input=data,capture_output=True,timeout=90)
 if r.returncode and not allowed:raise RuntimeError(r.stderr.decode(errors='replace')[-500:])
 return r

def valid_search_item(item):
 name=item.get('name');target=item.get('book_url')
 if not isinstance(name,str) or not name.strip() or not isinstance(target,str):return False
 try:
  parsed=urllib.parse.urlparse(target)
  return parsed.scheme in ['http','https'] and bool(parsed.netloc)
 except ValueError:return False

try:
 for name,image,port,extra in [(names[0],'postgres:16','5432',['--tmpfs','/var/lib/postgresql/data:rw','-e','POSTGRES_DB=yreader_test','-e','POSTGRES_HOST_AUTH_METHOD=trust']),(names[1],'redis:7-alpine','6379',[])]:
  imageid=docker('image','inspect',image,'--format','{{.Id}}').stdout.decode().strip()
  docker('run','-d','--pull=never','--name',name,'--label',label,'-p','127.0.0.1::'+port,*extra,imageid)
 for _ in range(45):
  r=docker('exec',names[0],'pg_isready','-h','127.0.0.1','-U','postgres','-d','yreader_test',allowed=True)
  if r.returncode==0:break
  time.sleep(1)
 else:raise RuntimeError('isolated database unavailable')
 docker('exec','-i',names[0],'psql','-Xq','-v','ON_ERROR_STOP=1','-U','postgres','-d','yreader_test',data=(root/'docs/schema.sql').read_bytes())
 ports=[json.loads(docker('inspect',n).stdout)[0]['NetworkSettings']['Ports'][k+'/tcp'][0]['HostPort'] for n,k in zip(names,['5432','6379'])]
 env=os.environ.copy();password=secrets.token_urlsafe(24)
 env.pop('E2E_BASE_URL',None)
 env['YREADER_SANDBOX_SOCKETS']=''
 env['LOCAL_IMPORT_TIMEOUT']='90s'
 with socket.socket() as reservation:
  reservation.bind(('127.0.0.1',0));api_port=reservation.getsockname()[1]
 env.update(DATABASE_URL=f'postgres://postgres:@127.0.0.1:{ports[0]}/yreader_test?sslmode=disable',REDIS_ADDR='127.0.0.1:'+ports[1],REDIS_URL='redis://127.0.0.1:'+ports[1]+'/0',ENVIRONMENT='production',PORT='55873',JWT_SECRET=secrets.token_hex(32),YREADER_HEADER_KEY_KID='primary',YREADER_HEADER_KEY_primary=base64.b64encode(secrets.token_bytes(32)).decode(),SEED_USERNAME='stage3-original',SEED_PASSWORD=password,STORAGE_DIR=str(out/'storage'))
 env['PORT']=str(api_port)
 (out/'tmp').mkdir()
 env['TMP']=str(out/'tmp');env['TEMP']=str(out/'tmp')
 log=(out/'server.log').open('wb')
 server=subprocess.Popen([str(a.server.resolve())],env=env,stdout=log,stderr=log,creationflags=subprocess.CREATE_NO_WINDOW)
 for _ in range(60):
  if server.poll() is not None:raise RuntimeError('isolated server startup failed')
  try:
   if urllib.request.urlopen(f'http://127.0.0.1:{api_port}/readyz',timeout=1).status==200:break
  except Exception:pass
  time.sleep(1)
 else:raise RuntimeError('server not ready')
 if a.sources_dir:
  base=f'http://127.0.0.1:{api_port}/api/v1';token=None
  def api(method,path,body=None):
   headers={'Content-Type':'application/json'}
   if token:headers['Authorization']='Bearer '+token
   req=urllib.request.Request(base+path,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method)
   try:
    with urllib.request.urlopen(req,timeout=25) as resp:return resp.status,json.load(resp)
   except urllib.error.HTTPError as e:return e.code,json.load(e)
  status,auth=api('POST','/auth/login',{'username':'stage3-original','password':password});token=auth.get('access_token')
  if status!=200 or not token:raise RuntimeError('source acceptance login failed')
  external=[]
  for sourcefile in sorted(a.sources_dir.glob('*.json')):
   data=sourcefile.read_bytes();items=json.loads(data)
   status,preview=api('POST','/sources/import',{'format':'legado_json','data':data.decode(),'dry_run':True,'overwrite':'skip'})
   record={'file':sourcefile.name,'sha256':hashlib.sha256(data).hexdigest(),'entries':len(items),'preview_status':status,'preview':preview,'searches':[]};external.append(record)
   # Untrusted native JS must not be executed in the Windows API process.
   candidates=[s for s in items if isinstance(s,dict) and s.get('searchUrl') and not any(x in str(s.get('searchUrl'))+str(s.get('ruleSearch')) for x in ['@js','<js>','java.','eval(','function','=>'])][:2]
   for source in candidates:
    selected=dict(source);selected['header']={};selected['enabled']=True
    status,imported=api('POST','/sources/import',{'format':'legado_json','data':json.dumps([selected]),'dry_run':False,'overwrite':'skip'})
    status,page=api('GET','/sources?page_size=100')
    found=next((s for s in page.get('items',[]) if s.get('book_source_url')==source.get('bookSourceUrl')),None)
    attempt={'name':source.get('bookSourceName'),'import_result':imported,'status':status,'auth_headers_removed':True}
    if found:
     start=time.monotonic();st,search=api('GET','/search?'+urllib.parse.urlencode({'keyword':'三国演义','source_ids':found['id']}))
     attempt.update(search_status=st,seconds=round(time.monotonic()-start,3),total=search.get('total',0),failure_codes=[f.get('code') for f in search.get('failures',[])])
     attempt['valid_result_count']=sum(valid_search_item(item) for group in search.get('groups',[]) for item in group.get('items',[]))
    record['searches'].append(attempt)
  (out/'external-source-results.json').write_text(json.dumps(external,ensure_ascii=False,indent=2),encoding='utf-8')
  result['external_sources_checked']=len(external)
  result['external_search_successes']=sum(s.get('valid_result_count',0)>0 and not s.get('failure_codes') for r in external for s in r['searches'])
  result['external_full_reading_gate_passed']=False
 epub=out/'original.epub'
 with zipfile.ZipFile(epub,'w') as z:
  z.writestr('mimetype','application/epub+zip')
  z.writestr('META-INF/container.xml','<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>')
  z.writestr('book.opf','<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">original-stage3</dc:identifier><dc:title>原创中文验收</dc:title><dc:language>zh-CN</dc:language><dc:creator>YLReader原创测试</dc:creator></metadata><manifest>'+''.join(f'<item id="c{i}" href="c{i}.xhtml" media-type="application/xhtml+xml"/>' for i in range(3))+'</manifest><spine>'+''.join(f'<itemref idref="c{i}"/>' for i in range(3))+'</spine></package>')
  opf=z.read('book.opf').decode().replace('<manifest>','<manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>').replace('</metadata>','<meta property="dcterms:modified">2026-11-15T00:00:00Z</meta></metadata>')
  # Replace the OPF without duplicate ZIP members.
  members={info.filename:z.read(info.filename) for info in z.infolist() if info.filename!='book.opf'}
 # Reopen to keep mimetype first and avoid ambiguous duplicate names.
 with zipfile.ZipFile(epub,'w') as z:
  for name,data in members.items():z.writestr(name,data)
  z.writestr('book.opf',opf)
  z.writestr('nav.xhtml','<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>目录</title></head><body><nav epub:type="toc"><ol>'+''.join(f'<li><a href="c{i}.xhtml">第{i+1}章</a></li>' for i in range(3))+'</ol></nav></body></html>')
  for i,title in enumerate(['山间的晨光','河岸的午后','星光下的书页']):z.writestr(f'c{i}.xhtml','<html xmlns="http://www.w3.org/1999/xhtml"><head><title>'+title+'</title></head><body><h1>'+title+'</h1>'+''.join(f'<p>第{n}段。{title}，我们在这里继续原创的故事。😀</p>' for n in range(100))+'</body></html>')
 numbered=out/'原创编号验收.txt'
 numbered.write_text(''.join(f'第{i}章 原创编号验收\n'+''.join(f'原创编号{i:03d}。这是独立编写的验收段落{n}，用于核对章节身份和中文排版。😀\n' for n in range(40))+'\n' for i in range(1,121)),encoding='utf-8')
 result['original_samples']={'numbered_txt':{'sha256':hashlib.sha256(numbered.read_bytes()).hexdigest(),'expected_chapters':120},'epub':{'sha256':hashlib.sha256(epub.read_bytes()).hexdigest(),'expected_chapters':3}}
 env.update(E2E_USERNAME='stage3-original',E2E_PASSWORD=password,E2E_DATABASE_URL=env['DATABASE_URL'],E2E_API_TARGET=f'http://127.0.0.1:{api_port}',E2E_STAGE3_EPUB=str(epub),E2E_STAGE3_TXT=str(numbered),E2E_STAGE3_READ_SECONDS=str(a.reading_seconds))
 env.pop('E2E_PRODUCTION',None)
 env['E2E_READING_PREVIEW']='1' if a.production_preview else '0'
 env.update(E2E_DB_CONTAINER=names[0],E2E_DB_USER='postgres',E2E_DB_NAME='yreader_test')
 command=['cmd','/c','npm','run','test:e2e']+(['--','tests/stage3-reading.spec.ts'] if a.reading_only or a.numbered_only else [])
 if a.numbered_only:command+=['--grep','original numbered TXT']
 r=subprocess.run(command,cwd=root/'frontend',env=env,capture_output=True,timeout=max(900,a.reading_seconds+600)) if not a.sources_only else subprocess.CompletedProcess(command,0,b'',b'')
 (out/'browser.stdout').write_bytes(r.stdout);(out/'browser.stderr').write_bytes(r.stderr)
 if not a.sources_only:
  for index, diagnostic in enumerate((root/'frontend/test-results').glob('**/reading-diagnostics.json')):
   (out/f'reading-diagnostics-{index}.json').write_bytes(diagnostic.read_bytes())
 result['browser_exit']=r.returncode;result['passed']=r.returncode==0
finally:
 if server is not None:
  server.terminate();server.wait(timeout=15)
 if log:log.close()
 for name in names:
  r=docker('inspect',name,'--format','{{index .Config.Labels "ylreader.stage3"}}',allowed=True)
  ok=r.returncode!=0 or (r.stdout.decode().strip()==ident and docker('rm','-f','-v',name,allowed=True).returncode==0)
  result['cleanup'][name]=ok
 result['passed']=result['passed'] and all(result['cleanup'].values())
 (out/'result.json').write_text(json.dumps(result,indent=2),encoding='utf-8');print(out/'result.json')
 if not result['passed']:raise RuntimeError('stage3 automated acceptance failed; inspect local logs')
