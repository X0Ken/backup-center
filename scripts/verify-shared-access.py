#!/usr/bin/env python3
"""Verify NFS and iSCSI from two remote Linux clients; retain all verification data.
Requires SSH/sudo access, nfs-utils and iscsi-initiator-utils on the clients.
Creates only uniquely named repositories, targets, mounts and test payloads.
"""
import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import ssl
import subprocess
import time
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true', required=True)
    parser.add_argument('--url', required=True)
    parser.add_argument('--certificate', required=True)
    parser.add_argument('--username', required=True)
    parser.add_argument('--password-file', required=True)
    parser.add_argument('--ssh-user', default='opencloudos')
    parser.add_argument('--ssh-password-file', required=True)
    parser.add_argument('--ssh-known-hosts', required=True)
    parser.add_argument('--clients', nargs=2, required=True, metavar=('CLIENT1', 'CLIENT2'))
    parser.add_argument('--client-addresses', nargs=2, required=True, metavar=('IP1', 'IP2'))
    parser.add_argument('--storage-address', required=True)
    args = parser.parse_args()
    if len(set(args.clients)) != 2 or len(set(args.client_addresses)) != 2:
        parser.error('two distinct clients and client addresses are required')
    run = time.strftime('%Y%m%d') + '-' + uuid.uuid4().hex[:6]
    prefix = 'accept-' + run
    env = os.environ.copy()
    env['SSHPASS'] = Path(args.ssh_password_file).read_text().strip()
    context = ssl.create_default_context(cafile=args.certificate)
    opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=context), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    csrf = ''

    def api(path, method='GET', data=None):
        req = urllib.request.Request(args.url.rstrip('/') + '/api' + path, method=method,
            data=json.dumps(data).encode() if data is not None else None,
            headers={'Content-Type': 'application/json', 'X-CSRF-Token': csrf})
        with opener.open(req, timeout=90) as response:
            return json.load(response)

    def job(path, data, method='POST'):
        result = api(path, method, data)
        for _ in range(240):
            current = api('/jobs/' + str(result['id']))
            if current['status'] == 'succeeded':
                return current
            if current['status'] in ('failed', 'interrupted'):
                raise RuntimeError(current['error'])
            time.sleep(.25)
        raise RuntimeError('job timeout: ' + str(result['id']))

    def remote(host, code, root=True):
        command = 'sudo python3 -' if root else 'python3 -'
        result = subprocess.run(['sshpass', '-e', 'ssh', '-o', 'UserKnownHostsFile=' + args.ssh_known_hosts, '-o', 'StrictHostKeyChecking=yes', '-o', 'ConnectTimeout=10',
            args.ssh_user + '@' + host, command], input=code, text=True, capture_output=True, env=env, timeout=120)
        if result.returncode:
            raise RuntimeError(f'{host}: {result.stderr}\n{result.stdout}')
        return json.loads(result.stdout)

    def repository(name, kind):
        job('/repositories', {'name': name, 'kind': kind, 'description': '跨节点共享访问验收 · 保留校验数据 ' + run,
            'volume_size': '512M', 'properties': {'compression': 'lz4', **({'quota': '2G'} if kind == 'filesystem' else {})}})
        repo = next(x['repository'] for x in api('/repositories') if x['repository']['name'] == name)
        api('/policies', 'PUT', {'repository_id': repo['id'], 'enabled': False, 'keep_daily': 7})
        return repo

    def snapshot(repo):
        result = job('/actions/snapshot.create', {'repository_id': repo['id']})
        name = result['result']
        job('/actions/snapshot.hold', {'repository_id': repo['id'], 'snapshot': name})
        return name

    def access(repo, **fields):
        job('/exports', {'repository_id': repo['id'], 'enabled': True, **fields}, 'PUT')
        return next(x for x in api('/exports') if x['repository_id'] == repo['id'])

    def mount_nfs(host, source, point):
        remote(host, f'''import subprocess,pathlib,json
p=pathlib.Path({point!r});p.mkdir(parents=True,exist_ok=False)
subprocess.run(['mount','-t','nfs','-o','vers=4,hard,timeo=100,retrans=2',{source!r},str(p)],check=True,timeout=30)
print(json.dumps({{'mountpoint':str(p)}}))
''')

    def payload(host, point, name):
        return remote(host, f'''import pathlib,os,hashlib,json
p=pathlib.Path({point!r})/{name!r}
data=os.urandom(1024*1024)
with p.open('xb') as f:f.write(data);f.flush();os.fsync(f.fileno())
digest=hashlib.sha256(data).hexdigest()
(p.parent/(p.name+'.sha256')).write_text(digest+'  '+p.name+'\\n')
print(json.dumps({{'name':p.name,'sha256':digest,'bytes':len(data)}}))
''', root=False)

    def verify_payloads(host, point, expected):
        remote(host, f'''import pathlib,hashlib,json
p=pathlib.Path({point!r})
expected={expected!r}
for item in expected:
 data=(p/item['name']).read_bytes()
 assert len(data)==item['bytes'] and hashlib.sha256(data).hexdigest()==item['sha256']
print(json.dumps({{'verified':len(expected)}}))
''', root=False)

    csrf = api('/auth/login', 'POST', {'username': args.username, 'password': Path(args.password_file).read_text().strip()})['csrf']
    clients = []
    for host, address in zip(args.clients, args.client_addresses):
        info = remote(host, '''import pathlib,socket,json
raw=pathlib.Path('/etc/iscsi/initiatorname.iscsi').read_text()
iqn=next(s.split('=',1)[1].strip() for s in raw.splitlines() if s.startswith('InitiatorName='))
print(json.dumps({'host':socket.gethostname(),'iqn':iqn}))
''')
        clients.append({'ssh': host, 'address': address, **info})
    if clients[0]['iqn'] == clients[1]['iqn']:
        raise RuntimeError('Client initiator IQNs must be unique')
    summary = {'run': run, 'clients': clients, 'repositories': [], 'snapshots': [], 'payloads': [], 'iscsi_targets': []}
    print('Keeping all verification data. Run: ' + run, flush=True)
    fs = repository(prefix + '-nfs', 'filesystem')
    summary['repositories'].append(fs)
    access(fs, clients=' '.join(args.client_addresses), read_only=False)
    mount = '/mnt/backup-center-acceptance/' + run + '/nfs'
    expected = []
    for client in clients:
        mount_nfs(client['ssh'], args.storage_address + ':/srv/backup-center/repos/' + fs['name'], mount)
        expected.append(payload(client['ssh'], mount, client['host'] + '-nfs.bin'))
    for client in clients:
        verify_payloads(client['ssh'], mount, expected)
    summary['payloads'].extend(expected)
    snap = snapshot(fs)
    summary['snapshots'].append(snap)
    clone_name = prefix + '-snapshot'
    job('/actions/restore.clone', {'repository_id': fs['id'], 'snapshot': snap, 'name': clone_name})
    clone = next(x['repository'] for x in api('/repositories') if x['repository']['name'] == clone_name)
    summary['repositories'].append(clone)
    access(clone, clients=' '.join(args.client_addresses), read_only=True)
    clone_mount = '/mnt/backup-center-acceptance/' + run + '/snapshot-nfs'
    for client in clients:
        mount_nfs(client['ssh'], args.storage_address + ':/srv/backup-center/repos/' + clone_name, clone_mount)
        verify_payloads(client['ssh'], clone_mount, expected)
        remote(client['ssh'], f'''import pathlib,errno,json
try:(pathlib.Path({clone_mount!r})/'must-not-write').write_text('readonly-test')
except OSError as e:assert e.errno==errno.EROFS
else:raise RuntimeError('Read-only export accepted a write')
print(json.dumps({{'readonly':True}}))
''', root=False)
    print('PASS: both clients wrote/read NFS data and read the snapshot clone; read-only writes rejected', flush=True)
    for client in clients:
        vol = repository(prefix + '-' + client['host'] + '-iscsi', 'volume')
        summary['repositories'].append(vol)
        export = access(vol, initiators=client['iqn'])
        iqn = export['iqn']
        summary['iscsi_targets'].append({'client': client['host'], 'iqn': iqn})
        portal = args.storage_address + ':3260'
        device = '/dev/disk/by-path/ip-' + portal + '-iscsi-' + iqn + '-lun-0'
        point = '/mnt/backup-center-acceptance/' + run + '/iscsi'
        remote(client['ssh'], f'''import pathlib,subprocess,time,json
run=lambda cmd:subprocess.run(cmd,check=True,capture_output=True,text=True,timeout=40)
iqn={iqn!r};portal={portal!r};device={device!r};point={point!r}
run(['iscsiadm','-m','node','-T',iqn,'-p',portal,'--op','new'])
run(['iscsiadm','-m','node','-T',iqn,'-p',portal,'--login'])
run(['udevadm','settle'])
for _ in range(50):
 if pathlib.Path(device).exists():break
 time.sleep(.1)
assert pathlib.Path(device).exists(),device
assert int(run(['blockdev','--getsize64',device]).stdout.strip())==512*1024*1024
assert len(run(['lsblk','-nr','-o','NAME',device]).stdout.splitlines())==1
probe=subprocess.run(['blkid','-p',device],capture_output=True,text=True)
assert probe.returncode==2 and not probe.stdout,'Refusing to format a device with an existing signature'
assert subprocess.run(['findmnt','-rn','-S',str(pathlib.Path(device).resolve())],capture_output=True).returncode==1
run(['mkfs.ext4','-L','bc-acceptance',device])
pathlib.Path(point).mkdir(parents=True,exist_ok=False)
run(['mount',device,point]);run(['chown','1000:1000',point])
print(json.dumps({{'device':device,'resolved':str(pathlib.Path(device).resolve()),'mount':point}}))
''')
        item = payload(client['ssh'], point, client['host'] + '-iscsi.bin')
        summary['payloads'].append(item)
        # Snapshot the unmounted filesystem, then reconnect and read from the same LUN.
        remote(client['ssh'], f'''import subprocess,json
subprocess.run(['umount',{point!r}],check=True)
print(json.dumps({{'unmounted':True}}))
''')
        summary['snapshots'].append(snapshot(vol))
        remote(client['ssh'], f'''import subprocess,pathlib,time,json
run=lambda cmd:subprocess.run(cmd,check=True,capture_output=True,text=True,timeout=40)
run(['iscsiadm','-m','node','-T',{iqn!r},'-p',{portal!r},'--logout'])
run(['iscsiadm','-m','node','-T',{iqn!r},'-p',{portal!r},'--login'])
run(['udevadm','settle'])
for _ in range(50):
 if pathlib.Path({device!r}).exists():break
 time.sleep(.1)
run(['mount',{device!r},{point!r}])
print(json.dumps({{'reconnected':True}}))
''')
        verify_payloads(client['ssh'], point, [item])
        print('PASS: ' + client['host'] + ' iSCSI filesystem survived logout/login and checksum matches', flush=True)
    for index, client in enumerate(clients):
        foreign = summary['iscsi_targets'][1-index]['iqn']
        remote(client['ssh'], f'''import subprocess,json
base=['iscsiadm','-m','node','-T',{foreign!r},'-p',{(args.storage_address+':3260')!r}]
run=lambda tail:subprocess.run(base+tail,capture_output=True,text=True,timeout=20)
created=run(['--op','new']);assert created.returncode==0,created.stderr
try:
 run(['--op','update','-n','node.session.initial_login_retry_max','-v','1'])
 result=run(['--login'])
 if result.returncode==0:
  run(['--logout']);raise RuntimeError('Foreign client was allowed into another client target')
 assert 'authorization' in result.stderr.lower(),result.stderr
finally:
 removed=run(['--op','delete']);assert removed.returncode==0,removed.stderr
print(json.dumps({{'foreign_target_denied':True}}))
''')
    print('PASS: each client is denied access to the other client iSCSI target', flush=True)
    summary['mounts'] = {'nfs': mount, 'snapshot_nfs': clone_mount, 'iscsi': '/mnt/backup-center-acceptance/' + run + '/iscsi'}
    print(json.dumps(summary, ensure_ascii=False, indent=2), flush=True)


if __name__ == '__main__':
    main()
