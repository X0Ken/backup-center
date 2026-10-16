#!/usr/bin/env python3
"""Verify replication between two real appliances; retain uniquely named fixtures.
Requires explicit --run, protected temporary administrator credentials and SSH
password files. Uses only newly created repositories for transfer fault injection.
Credentials JSON: {"username": "...", "password": "..."} for the same temporary
verification account on both nodes. Existing administrator accounts are untouched.
"""
import argparse
import base64
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import ssl
import subprocess
import time
import urllib.request


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--run',action='store_true',required=True)
    p.add_argument('--source',required=True)
    p.add_argument('--target',required=True)
    p.add_argument('--target-peer-url',required=True)
    p.add_argument('--target-name',default='异地验收目标')
    p.add_argument('--source-ssh',required=True)
    p.add_argument('--target-ssh',required=True)
    p.add_argument('--ssh-user',default='opencloudos')
    p.add_argument('--ssh-password-file',type=Path,required=True)
    p.add_argument('--credentials-file',type=Path,required=True)
    p.add_argument('--source-certificate',type=Path,required=True)
    p.add_argument('--target-certificate',type=Path,required=True)
    args=p.parse_args()
    for file in (args.credentials_file,args.ssh_password_file):
        if file.stat().st_mode & 0o077:raise RuntimeError('Credential files must have mode 0600')
    credentials=json.loads(args.credentials_file.read_text())
    class API:
        def __init__(self,url,certificate):
            self.url=url;self.csrf=''
            self.opener=urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(certificate))),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
            self.csrf=self.call('/auth/login','POST',{'username':credentials['username'],'password':credentials['password']})['csrf']
        def call(self,path,method='GET',data=None):
            req=urllib.request.Request(self.url+'/api'+path,method=method,data=json.dumps(data).encode() if data is not None else None,headers={'Content-Type':'application/json','X-CSRF-Token':self.csrf})
            with self.opener.open(req,timeout=90) as r:return json.load(r)
        def wait(self,j):
            for _ in range(1200):
                job=self.call('/jobs/'+str(j['id']))
                if job['status'] in ('failed','interrupted','succeeded'):
                    assert job['status']=='succeeded',job
                    return job
                time.sleep(.1)
            raise RuntimeError('job timeout')
        def action(self,kind,body):return self.wait(self.call('/actions/'+kind,'POST',body))
    def remote(host,code):
        r=subprocess.run(['sshpass','-f',str(args.ssh_password_file),'ssh','-o','StrictHostKeyChecking=accept-new',args.ssh_user+'@'+host,'sudo python3 -'],input=code,text=True,capture_output=True,timeout=180)
        if r.returncode:raise RuntimeError(r.stderr+r.stdout)
        return r.stdout.strip()
    src=API(args.source,args.source_certificate);dst=API(args.target,args.target_certificate)
    version=src.call('/system')['version'];assert dst.call('/system')['version']==version
    code=dst.call('/peers/invitation','POST',{})['code']
    src.call('/peers','POST',{'name':args.target_name,'url':args.target_peer_url,'code':code})
    prefix='cross-'+time.strftime('%Y%m%d')+'-'+secrets.token_hex(3)
    def create(name,kind='filesystem',encrypted=False):
        q={'name':name,'kind':kind,'encrypted':encrypted,'properties':{'quota':'512M','compression':'lz4'} if kind=='filesystem' else {},'volume_size':'64M' if kind=='volume' else ''}
        src.wait(src.call('/repositories','POST',q))
        return next(r['repository'] for r in src.call('/repositories') if r['repository']['name']==name)
    repo=create(prefix);root='/srv/backup-center/repos/'+prefix
    expected=hashlib.sha256(b'initial-data\n').hexdigest()
    remote(args.source_ssh,f"from pathlib import Path;p=Path({root!r});(p/'proof.txt').write_text('initial-data\\n');(p/'remove.txt').write_text('keep-in-baseline\\n')")
    started=time.monotonic();rep=src.call('/replications','PUT',{'repository_id':repo['id']});assert time.monotonic()-started<5
    def current():return next(r for r in src.call('/replications') if r['id']==rep['id'])
    def wait_ready(snapshot):
        for _ in range(1800):
            row=current()
            if row['state']=='ready' and row['last_snapshot']==snapshot:return row
            time.sleep(.1)
        raise RuntimeError(current())
    for _ in range(600):
        waiting=current()
        if waiting['state']=='waiting' and waiting['remote_repository_id']:break
        time.sleep(.1)
    assert waiting['remote_repository_id'] and waiting['state']=='waiting'
    first=src.action('snapshot.create',{'repository_id':repo['id']})['result'];ready=wait_ready(first);rid=ready['remote_repository_id']
    receiver=next(r['repository'] for r in dst.call('/repositories') if r['repository']['id']==rid)
    assert receiver['display_name']=='【异地复制】'+prefix and receiver['replica']
    print('PASS two physical hosts, same version, paired over storage LAN, asynchronous remote repository registration and full copy',flush=True)
    remote(args.source_ssh,f"from pathlib import Path;p=Path({root!r});(p/'proof.txt').write_text('updated-data\\n');(p/'remove.txt').unlink()")
    second=src.action('snapshot.create',{'repository_id':repo['id']})['result'];assert wait_ready(second)['remote_repository_id']==rid
    baseline_clone=prefix+'-baseline'
    dst.action('restore.clone',{'repository_id':rid,'snapshot':receiver['dataset']+'@'+first.split('@')[1],'name':baseline_clone})
    result=remote(args.target_ssh,f"from pathlib import Path;import hashlib;p=Path('/srv/backup-center/repos')/{baseline_clone!r};assert (p/'remove.txt').exists();print(hashlib.sha256((p/'proof.txt').read_bytes()).hexdigest())")
    assert result==expected
    print('PASS automatic incremental snapshot copy; original remote snapshot restores unchanged baseline data',flush=True)
    src.call('/replications','PUT',{'id':rep['id'],'enabled':False})
    remote(args.source_ssh,f"from pathlib import Path;import os,hashlib;p=Path({root!r})/'resume.bin';p.write_bytes(os.urandom(64*1024*1024));print(hashlib.sha256(p.read_bytes()).hexdigest())")
    third=src.action('snapshot.create',{'repository_id':repo['id']})['result']
    ca=args.target_certificate.read_text()
    injection=f'''
import sqlite3,base64,json,subprocess,ssl,http.client,urllib.parse
from pathlib import Path
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
c=sqlite3.connect('/var/lib/backup-center/backup-center.db')
row=c.execute('select p.credential,p.url from platform_peers p join replications r on r.peer_id=p.id where r.id=?',({rep['id']},)).fetchone()
blob=base64.b64decode(row[0]);secret=json.loads(AESGCM(Path('/var/lib/backup-center/keys/.sync-master.key').read_bytes()).decrypt(blob[:12],blob[12:],b'backup-center-sync-v1'))['password']
subprocess.run(['zfs','hold','bc-transfer-'+str({rep['id']}),{third!r}],check=True)
c.execute('update replications set transfer_snapshot=? where id=?',({third!r},{rep['id']}));c.commit();c.close()
sender=subprocess.Popen(['zfs','send','-w','-i',{second!r},{third!r}],stdout=subprocess.PIPE,stderr=subprocess.DEVNULL)
chunk=sender.stdout.read(2*1024*1024);sender.kill();sender.wait()
u=urllib.parse.urlsplit(row[1]);conn=http.client.HTTPSConnection(u.hostname,u.port or 443,context=ssl.create_default_context(cadata={ca!r}),timeout=60)
conn.putrequest('POST','/api/peer/repositories/{rid}/receive')
for k,v in {{'Authorization':'Bearer '+secret,'X-Backup-Center-Version':{version!r},'Content-Type':'application/octet-stream','Content-Length':str(len(chunk)+10*1024*1024)}}.items():conn.putheader(k,v)
conn.endheaders();conn.send(chunk);conn.close();print('Injected interrupted stream')
'''
    remote(args.source_ssh,injection)
    for _ in range(200):
        token=remote(args.target_ssh,f"import subprocess;v=subprocess.check_output(['zfs','get','-H','-o','value','receive_resume_token',{receiver['dataset']!r}],text=True).strip();print(v not in ('','-'))")
        if token=='True':break
        time.sleep(.1)
    assert token=='True'
    assert current()['last_snapshot']==second
    for host in (args.source_ssh,args.target_ssh):remote(host,"import subprocess;subprocess.run(['systemctl','restart','backupd'],check=True)")
    for _ in range(100):
        try:src.call('/auth/me');dst.call('/auth/me');break
        except Exception:time.sleep(.2)
    src.call('/replications','PUT',{'id':rep['id'],'enabled':True})
    ready=wait_ready(third);assert ready['remote_repository_id']==rid and ready['bytes']<64*1024*1024
    latest_clone=prefix+'-restore';dst.action('restore.clone',{'repository_id':rid,'snapshot':receiver['dataset']+'@'+third.split('@')[1],'name':latest_clone})
    for host,path in [(args.source_ssh,root),(args.target_ssh,'/srv/backup-center/repos/'+latest_clone)]:
        digest=remote(host,f"from pathlib import Path;import hashlib;p=Path({path!r});assert (p/'proof.txt').read_text()=='updated-data\\n';assert not (p/'remove.txt').exists();print(hashlib.sha256((p/'resume.bin').read_bytes()).hexdigest())")
        if host==args.source_ssh:source_digest=digest
        else:assert digest==source_digest
    print('PASS real cross-host upload interruption, durable resume token, both service restarts, automatic resume and SHA-256 verified recovery',flush=True)
    for kind,encrypted in [('filesystem',True),('volume',False)]:
        name=prefix+('-encrypted' if encrypted else '-volume');rr=create(name,kind,encrypted);payload=secrets.token_bytes(4096);digest=hashlib.sha256(payload).hexdigest()
        path='/srv/backup-center/repos/'+name+'/proof' if encrypted else '/dev/zvol/'+rr['dataset']
        remote(args.source_ssh,f"import base64,os,time;from pathlib import Path;p=Path({path!r});\nfor _ in range(100):\n if p.parent.exists() if {encrypted!r} else p.exists():break\n time.sleep(.1)\nwith p.open('wb',buffering=0) as f:f.write(base64.b64decode({base64.b64encode(payload).decode()!r}));os.fsync(f.fileno())")
        snap=src.action('snapshot.create',{'repository_id':rr['id']})['result']
        rep=src.call('/replications','PUT',{'repository_id':rr['id'],'remote_name':'【异地复制】'+name});result=wait_ready(snap)
        remote_repo=next(r['repository'] for r in dst.call('/repositories') if r['repository']['id']==result['remote_repository_id'])
        clone=name+'-restore';dst.action('restore.clone',{'repository_id':remote_repo['id'],'snapshot':remote_repo['dataset']+'@'+snap.split('@')[1],'name':clone})
        cloned=next(r['repository'] for r in dst.call('/repositories') if r['repository']['name']==clone)
        path='/srv/backup-center/repos/'+clone+'/proof' if encrypted else '/dev/zvol/'+cloned['dataset']
        actual=remote(args.target_ssh,f"import hashlib,time;from pathlib import Path;p=Path({path!r});\nfor _ in range(100):\n if p.exists():break\n time.sleep(.1)\nwith p.open('rb',buffering=0) as f:print(hashlib.sha256(f.read(4096)).hexdigest())")
        assert actual==digest
        print('PASS cross-host', 'native encryption and recovery key' if encrypted else 'block volume', 'copy and cloned recovery',flush=True)
    print('KEPT',prefix,'with live platform trust, replication tasks, source data, remote snapshots and recovered clones',flush=True)

if __name__=='__main__':main()
