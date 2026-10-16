#!/usr/bin/env python3
"""Real platform replication acceptance. Run as root on an isolated test pair.
Requires a dedicated source/target platform, local access to their state and pools,
and explicit --run. Creates uniquely named fixtures and retains all test data.
Never use production state paths: fault injection reads test-only scoped tokens.
"""
import argparse
import base64
import hashlib
import http.client
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import sqlite3
import ssl
import subprocess
import time
import urllib.request
import urllib.parse
from cryptography.hazmat.primitives.ciphers.aead import AESGCM


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--run', action='store_true', required=True)
    p.add_argument('--source', required=True)
    p.add_argument('--target', required=True)
    p.add_argument('--source-state', type=Path, required=True)
    p.add_argument('--target-state', type=Path, required=True)
    p.add_argument('--password-file', type=Path, required=True)
    p.add_argument('--username', default='verify')
    p.add_argument('--certificate', default='/etc/backup-center/tls/server.crt')
    args = p.parse_args()
    for state in (args.source_state,args.target_state):
        if state.resolve() == Path('/var/lib/backup-center'):
            raise RuntimeError('Use dedicated acceptance instances, not production state')
    ctx = ssl.create_default_context(cafile=args.certificate)
    ctx.check_hostname = False  # Test listeners use loopback; certificate chain is verified.
    class API:
        def __init__(self, url):
            self.url=url;self.csrf='';self.opener=urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctx),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
            self.csrf=self.call('/auth/login','POST',{'username':args.username,'password':args.password_file.read_text().strip()})['csrf']
        def call(self,path,method='GET',data=None):
            req=urllib.request.Request(self.url+'/api'+path,method=method,data=json.dumps(data).encode() if data is not None else None,headers={'Content-Type':'application/json','X-CSRF-Token':self.csrf})
            with self.opener.open(req,timeout=90) as r:return json.load(r)
        def wait(self,job):
            for _ in range(600):
                j=self.call('/jobs/'+str(job['id']))
                if j['status'] in ('succeeded','failed','interrupted'):
                    assert j['status']=='succeeded',j
                    return j
                time.sleep(.1)
            raise RuntimeError('job timeout')
        def action(self,kind,body):return self.wait(self.call('/actions/'+kind,'POST',body))
    src,dst=API(args.source),API(args.target)
    invite=dst.call('/peers/invitation','POST',{})
    src.call('/peers','POST',{'url':args.target,'name':'隔离验收目标','code':invite['code']})
    prefix='peer-'+time.strftime('%Y%m%d')+'-'+secrets.token_hex(3)
    src.wait(src.call('/repositories','POST',{'name':prefix,'kind':'filesystem','properties':{'quota':'512M','compression':'lz4'}}))
    repo=next(r['repository'] for r in src.call('/repositories') if r['repository']['name']==prefix)
    mount=Path('/srv/backup-center/repos')/prefix
    payload=secrets.token_bytes(2*1024*1024);(mount/'proof.bin').write_bytes(payload)
    rep=src.call('/replications','PUT',{'repository_id':repo['id']})
    def current():return next(r for r in src.call('/replications') if r['id']==rep['id'])
    def wait_ready(snapshot):
        for _ in range(900):
            r=current()
            if r['state']=='ready' and r['last_snapshot']==snapshot:return r
            time.sleep(.1)
        raise RuntimeError(current())
    for _ in range(300):
        waiting=current()
        if waiting['remote_repository_id'] and waiting['state']=='waiting':break
        time.sleep(.1)
    assert waiting['remote_repository_id'] and waiting['state']=='waiting'
    print('PASS remote repository is registered asynchronously before the first snapshot',flush=True)
    first=src.action('snapshot.create',{'repository_id':repo['id']})['result']
    ready=wait_ready(first);remote_id=ready['remote_repository_id'];assert remote_id
    remote=next(r['repository'] for r in dst.call('/repositories') if r['repository']['id']==remote_id)
    assert remote['display_name']=='【异地复制】'+prefix
    assert remote['replica']
    print('PASS same-version pairing, asynchronous remote reservation and full replication',flush=True)
    (mount/'proof.bin').write_bytes(b'updated')
    second=src.action('snapshot.create',{'repository_id':repo['id']})['result'];ready=wait_ready(second)
    assert ready['remote_repository_id']==remote_id
    print('PASS snapshot-triggered incremental copy keeps the same remote repository ID',flush=True)
    src.call('/replications','PUT',{'id':rep['id'],'enabled':False})
    large=secrets.token_bytes(32*1024*1024);(mount/'resume.bin').write_bytes(large)
    third=src.action('snapshot.create',{'repository_id':repo['id']})['result']
    # Inject a real incomplete ZFS stream through the production HTTP receive API.
    c=sqlite3.connect(args.source_state/'backup-center.db')
    encrypted=c.execute("select credential from platform_peers where direction='outbound'").fetchone()[0]
    instance=c.execute("select value from settings where key='platform_instance_id'").fetchone()[0]
    ciphertext=base64.b64decode(encrypted);key=(args.source_state/'keys/.sync-master.key').read_bytes()
    token=json.loads(AESGCM(key).decrypt(ciphertext[:12],ciphertext[12:],b'backup-center-sync-v1'))['password']
    tag='bc-transfer-'+str(rep['id']);subprocess.run(['zfs','hold',tag,third],check=True)
    c.execute('update replications set transfer_snapshot=? where id=?',(third,rep['id']));c.commit();c.close()
    sender=subprocess.Popen(['zfs','send','-w','-i',second,third],stdout=subprocess.PIPE,stderr=subprocess.DEVNULL)
    chunk=sender.stdout.read(2*1024*1024);sender.kill();sender.wait()
    target=urllib.parse.urlparse(args.target)
    conn=http.client.HTTPSConnection(target.hostname,target.port,context=ctx,timeout=30)
    conn.putrequest('POST','/api/peer/repositories/'+str(remote_id)+'/receive')
    for k,v in {'Authorization':'Bearer '+token,'X-Backup-Center-Version':src.call('/system')['version'],'Content-Type':'application/octet-stream','Content-Length':str(len(chunk)+10*1024*1024)}.items():conn.putheader(k,v)
    conn.endheaders();conn.send(chunk);conn.close()
    for _ in range(100):
        resume=subprocess.check_output(['zfs','get','-H','-o','value','receive_resume_token',remote['dataset']],text=True).strip()
        if resume not in ('','-'):break
        time.sleep(.1)
    assert resume not in ('','-'),'real receive did not retain a resume token'
    # Restart both daemons before letting the persistent task resume.
    subprocess.run(['systemctl','restart','backup-center-peer-source-test','backup-center-peer-target-test'],check=True)
    for _ in range(100):
        try:src.call('/auth/me');dst.call('/auth/me');break
        except Exception:time.sleep(.1)
    src.call('/replications','PUT',{'id':rep['id'],'enabled':True})
    ready=wait_ready(third);assert ready['remote_repository_id']==remote_id
    assert subprocess.check_output(['zfs','get','-H','-o','value','receive_resume_token',remote['dataset']],text=True).strip()=='-'
    assert int(ready['bytes'])<len(large),'resume sent the entire file again'
    print('PASS incomplete HTTPS upload retains ZFS resume token; both services restart and task automatically resumes',flush=True)
    snap=remote['dataset']+'@'+third.split('@')[1]
    clone_name=prefix+'-restore';dst.action('restore.clone',{'repository_id':remote_id,'snapshot':snap,'name':clone_name})
    restored=Path('/srv/backup-center/repos')/clone_name
    assert (restored/'proof.bin').read_bytes()==b'updated'
    assert hashlib.sha256((restored/'resume.bin').read_bytes()).digest()==hashlib.sha256(large).digest()
    assert ready['remote_repository_id']==remote_id
    print('PASS snapshot clone restores exact full and resumed data; no duplicate remote repository',flush=True)
    src.call('/replications','PUT',{'id':rep['id'],'enabled':False})
    # Verify both supported repository types, including native encryption keys.
    for kind, encrypted in [('filesystem',True),('volume',False)]:
        name=prefix+('-encrypted' if encrypted else '-volume')
        q={'name':name,'kind':kind,'properties':{},'encrypted':encrypted}
        if kind=='volume':q['volume_size']='64M'
        src.wait(src.call('/repositories','POST',q))
        repo2=next(r['repository'] for r in src.call('/repositories') if r['repository']['name']==name)
        proof=secrets.token_bytes(4096)
        location=Path('/srv/backup-center/repos')/name/'encrypted-proof' if encrypted else Path('/dev/zvol')/repo2['dataset']
        for _ in range(100):
            if location.parent.exists() if encrypted else location.exists():break
            time.sleep(.1)
        with open(location,'wb',buffering=0) as stream:stream.write(proof);os.fsync(stream.fileno())
        snapshot=src.action('snapshot.create',{'repository_id':repo2['id']})['result']
        rep=src.call('/replications','PUT',{'repository_id':repo2['id'],'remote_name':'验收：'+name})
        ready=wait_ready(snapshot)
        rr=next(r['repository'] for r in dst.call('/repositories') if r['repository']['id']==ready['remote_repository_id'])
        clone_name=name+'-restore'
        dst.action('restore.clone',{'repository_id':rr['id'],'snapshot':rr['dataset']+'@'+snapshot.split('@')[1],'name':clone_name})
        clone=next(r['repository'] for r in dst.call('/repositories') if r['repository']['name']==clone_name)
        recovered=Path('/srv/backup-center/repos')/clone_name/'encrypted-proof' if encrypted else Path('/dev/zvol')/clone['dataset']
        for _ in range(100):
            if recovered.exists():break
            time.sleep(.1)
        with open(recovered,'rb',buffering=0) as stream:assert stream.read(4096)==proof
        src.call('/replications','PUT',{'id':rep['id'],'enabled':False})
        print('PASS', 'encrypted filesystem and automatic recovery key' if encrypted else 'block volume', 'replication and clone recovery',flush=True)
    print('KEPT acceptance fixtures:',prefix,'including source, remote and recovered repositories',flush=True)

if __name__=='__main__':main()
