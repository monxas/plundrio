#!/usr/bin/env python3
"""Build only the validator context from one clean source commit; never deploy apps."""
import argparse,datetime,fcntl,hashlib,json,os,pathlib,re,resource,shutil,subprocess,sys,tempfile

def sha(path):return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()
def run(argv,timeout=30,**kwargs):
 r=subprocess.run(argv,capture_output=True,timeout=timeout,**kwargs)
 if r.returncode:raise RuntimeError('Command failed: '+argv[0]+': '+r.stderr.decode(errors='replace')[-1000:])
 return r.stdout

def git(source,*args):
 return run(['git','-c','safe.directory='+str(source),'-C',str(source),*args]).decode().strip()
def fsync_dir(path):
 fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY)
 try:os.fsync(fd)
 finally:os.close(fd)
def limits():
 for key,value in [(resource.RLIMIT_AS,1024*2**20),(resource.RLIMIT_DATA,256*2**20),(resource.RLIMIT_CPU,15),(resource.RLIMIT_FSIZE,4*2**20)]:resource.setrlimit(key,(value,value))
 os.nice(10)
def fixtures(binary):
 assert pathlib.Path('/usr/bin/ffprobe').is_file(),'Install ffprobe before accepting the exported helper'
 with tempfile.TemporaryDirectory(prefix='media-validator-fixtures-')as d:
  root=pathlib.Path(d);p=root/'healthy.mp4'
  (root/'fixture.rgb').write_bytes(bytes([0,0,255])*64*64*30)
  run(['/usr/bin/ffmpeg','-nostdin','-v','error','-f','rawvideo','-pixel_format','rgb24','-video_size','64x64','-framerate','10','-i',str(root/'fixture.rgb'),'-frames:v','30','-filter_threads','1','-filter_complex_threads','1','-threads','1','-c:v','mpeg4','-pix_fmt','yuv420p',str(p)],timeout=20,preexec_fn=limits,env={'PATH':'/usr/bin:/bin','LC_ALL':'C','OMP_NUM_THREADS':'1','OPENBLAS_NUM_THREADS':'1'})
  b=p.read_bytes();(root/'renamed.txt').write_bytes(b);(root/'truncated.mp4').write_bytes(b[:len(b)//2]);(root/'archive.mp4').write_bytes(b'PK\x03\x04'+b'\x00'*32)
  results=[]
  for name,code,status in [('healthy.mp4',0,'accepted'),('healthy.mp4',0,'accepted'),('renamed.txt',0,'accepted'),('truncated.mp4',2,'rejected'),('truncated.mp4',2,'rejected'),('archive.mp4',3,'pending')]:
   r=subprocess.run([str(binary),'--root',str(root),'--state',str(root/'state'),'--path',str(root/name)],capture_output=True,timeout=25,env={'PATH':'/usr/bin:/bin','LC_ALL':'C'})
   assert len(r.stdout)<4096 and len(r.stderr)<4096
   x=json.loads(r.stdout);assert r.returncode==code and x['policy']==1 and x['status']==status,(name,code,x.get('reason'))
   results.append({'fixture':name,'status':status,'reason':x['reason'],'observed_at':x['observed_at']})
  assert results[0]['observed_at']==results[1]['observed_at'] and results[3]['observed_at']==results[4]['observed_at'],'Identity cache did not survive process restart'
  return results

def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('action',choices=['plan','build']);p.add_argument('--source',type=pathlib.Path,default=pathlib.Path(__file__).resolve().parent.parent);p.add_argument('--commit',required=True);p.add_argument('--output-root',type=pathlib.Path,required=True);args=p.parse_args()
 assert sys.platform=='linux','Build and acceptance execute on the Linux Docker host'
 source=args.source.resolve(strict=True);root=args.output_root.resolve();commit=args.commit.lower();assert re.fullmatch('[0-9a-f]{40}',commit)
 assert git(source,'rev-parse','HEAD')==commit and not git(source,'status','--porcelain'),'Source must be the exact clean planned commit'
 assert source not in [root]+list(root.parents),'Artifacts must remain outside source Git'
 dockerfile=source/'Dockerfile';match=re.search(r'^ARG GO_IMAGE=(.+)$',dockerfile.read_text(),re.M);assert match;go_image=match[1].strip();assert re.fullmatch(r'golang:[^\s]+@sha256:[0-9a-f]{64}',go_image),'Builder must be pinned by version and digest'
 target=root/(commit+'-linux-amd64-'+go_image.rsplit(':',1)[1][:12]);inputs={'schema':1,'repository':'https://github.com/monxas/plundrio','source_commit':commit,'platform':'linux/amd64','go_image':go_image,'recipe_sha256':sha(dockerfile),'builder_script_sha256':sha(__file__),'go_mod_sha256':sha(source/'go.mod'),'go_sum_sha256':sha(source/'go.sum'),'dockerignore_sha256':sha(source/'.dockerignore'),'target':'media-validator','build_flags':{'CGO_ENABLED':'0','GOOS':'linux','GOARCH':'amd64','trimpath':True,'buildvcs':False,'ldflags':'-s -w','GOMAXPROCS':'2','GOMEMLIMIT':'384MiB','package_parallelism':'2'}}
 plan={'inputs':inputs,'context_directory':str(target),'additional_contexts':{'mediavalidator':str(target)},'existing':target.exists(),'runtime_deployment':False}
 if args.action=='plan':print(json.dumps(plan,sort_keys=True));return
 root.mkdir(mode=0o755,parents=True,exist_ok=True)
 with (root/('.lock-'+target.name)).open('a')as lock:
  fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  if target.exists():
   manifest=json.loads((target/'manifest.json').read_text());assert manifest['inputs']==inputs and sha(target/'media-validate')==manifest['binary_sha256'],'Immutable artifact mismatch; do not overwrite'
   print(json.dumps({**plan,'existing':True,'cache_hit':True,'manifest_sha256':sha(target/'manifest.json'),'binary_sha256':manifest['binary_sha256']}));return
  stage=pathlib.Path(tempfile.mkdtemp(prefix='.export-',dir=root));log=root/('.build-'+target.name+'.log')
  try:
   command=['docker','build','--platform','linux/amd64','--build-arg','SOURCE_REVISION='+commit,'--target','media-validator','--output','type=local,dest='+str(stage),str(source)]
   with log.open('wb')as output:
    os.fchmod(output.fileno(),0o600);r=subprocess.run(command,stdout=output,stderr=subprocess.STDOUT,timeout=600);assert r.returncode==0,'Validator export failed; inspect '+str(log)
   assert git(source,'rev-parse','HEAD')==commit and not git(source,'status','--porcelain'),'Source changed during build'
   binary=stage/'media-validate';assert binary.is_file() and not binary.is_symlink() and {x.name for x in stage.iterdir()}=={'media-validate'}
   observed=fixtures(binary);manifest={'inputs':inputs,'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'binary_sha256':sha(binary),'binary_bytes':binary.stat().st_size,'fixtures':observed,'runtime_deployment':False}
   with (stage/'manifest.json').open('w')as f:json.dump(manifest,f,indent=2);f.flush();os.fsync(f.fileno())
   with binary.open('rb')as f:os.fsync(f.fileno())
   binary.chmod(0o555);(stage/'manifest.json').chmod(0o444);stage.chmod(0o555);fsync_dir(stage);os.rename(stage,target);fsync_dir(root)
   print(json.dumps({**plan,'existing':False,'cache_hit':False,'manifest_sha256':sha(target/'manifest.json'),'binary_sha256':manifest['binary_sha256'],'fixtures_passed':True}))
  finally:
   if stage.exists():stage.chmod(0o700);shutil.rmtree(stage)
if __name__=='__main__':main()
