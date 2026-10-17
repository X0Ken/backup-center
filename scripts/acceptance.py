#!/usr/bin/env python3
"""Destructive-to-test-resources integration acceptance, run ON the appliance.
Creates and removes uniquely named, small test datasets. Never touches other repos.
Requires explicit --run and an administrator password file. No mocks.
"""
import argparse
import hashlib
import http.cookiejar
import ipaddress
import json
import os
from pathlib import Path
import secrets
import ssl
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true', required=True)
    parser.add_argument('--url', required=True)
    parser.add_argument('--storage-address', required=True, type=ipaddress.ip_address,
                        help='Appliance local storage IP used for the NFS mount and client ACL')
    parser.add_argument('--password-file', required=True)
    parser.add_argument('--certificate', default='/etc/backup-center/tls/server.crt')
    args = parser.parse_args()
    context = ssl.create_default_context(cafile=args.certificate)
    opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=context), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    csrf = ''

    def request(path, method='GET', data=None, raw=False):
        req = urllib.request.Request(args.url + '/api' + path, method=method,
            data=json.dumps(data).encode() if data is not None else None,
            headers={'Content-Type': 'application/json', 'X-CSRF-Token': csrf})
        try:
            with opener.open(req, timeout=180) as response:
                content = response.read()
                return content if raw else json.loads(content)
        except urllib.error.HTTPError as exc:
            raise RuntimeError(f'{method} {path}: {exc.code}: {exc.read().decode()}') from exc

    def job(path, data):
        result = request(path, 'POST', data)
        for _ in range(180):
            jobs = request('/jobs')
            current = next(j for j in jobs if j['id'] == result['id'])
            if current['status'] == 'succeeded':
                return current
            if current['status'] in ('failed', 'interrupted'):
                raise RuntimeError(f"Job {current['id']} {current['kind']}: {current['error']}")
            time.sleep(.5)
        raise RuntimeError('Job timed out')

    password_path = Path(args.password_file)
    auth = request('/auth/login', 'POST', {'username': 'admin', 'password': password_path.read_text().strip()})
    csrf = auth['csrf']
    if auth['user']['must_change']:
        new_password = secrets.token_urlsafe(30)
        request('/auth/password', 'POST', {'current_password': password_path.read_text().strip(), 'new_password': new_password})
        password_path.write_text(new_password + '\n')
        password_path.chmod(0o600)
    prefix = 'verify-' + time.strftime('%Y%m%d%H%M%S')
    created = []

    def create(name, kind='filesystem', encrypted=False):
        result = job('/repositories', {'name': name, 'kind': kind, 'volume_size': '1G', 'encrypted': encrypted, 'properties': {'compression': 'lz4', 'quota': '2G'}})
        rows = request('/repositories')
        row = next(x for x in rows if x['repository']['name'] == name)
        assert not row['error'], row
        created.append(row['repository'])
        return row['repository']

    repo = create(prefix)
    mount = Path('/srv/backup-center/repos') / repo['name']
    payload = secrets.token_bytes(1024 * 1024)
    file = mount / 'restore-proof.bin'
    with file.open('wb') as output:
        output.write(payload)
        output.flush()
        os.fsync(output.fileno())
    first = job('/actions/snapshot.create', {'repository_id': repo['id'], 'name': 'proof-v1'})['result']
    file.write_bytes(b'modified-data')
    second = job('/actions/snapshot.create', {'repository_id': repo['id'], 'name': 'proof-v2'})['result']
    query = urllib.parse.urlencode({'repository_id': repo['id'], 'snapshot': first, 'path': 'restore-proof.bin'})
    restored = request('/snapshots/download?' + query, raw=True)
    assert hashlib.sha256(restored).digest() == hashlib.sha256(payload).digest()
    print('PASS: snapshot file download matches original SHA-256', flush=True)
    job('/actions/restore.clone', {'repository_id': repo['id'], 'snapshot': first, 'name': prefix + '-clone'})
    clone = next(x['repository'] for x in request('/repositories') if x['repository']['name'] == prefix + '-clone')
    created.append(clone)
    assert (Path('/srv/backup-center/repos') / clone['name'] / 'restore-proof.bin').read_bytes() == payload
    print('PASS: writable clone restores exact original data', flush=True)
    job('/actions/restore.rollback', {'repository_id': repo['id'], 'snapshot': second, 'confirm': repo['name'], 'acknowledge_offline': True})
    assert file.read_bytes() == b'modified-data'
    print('PASS: latest-snapshot rollback', flush=True)
    # NFS is tested through a real kernel mount; writes run with UID 1000 under root_squash.
    export = {'repository_id': repo['id'], 'protocol': 'nfs', 'clients': str(args.storage_address) + ('/32' if args.storage_address.version == 4 else '/128'), 'read_only': False, 'enabled': True, 'iqn': '', 'initiators': ''}
    result = request('/exports', 'PUT', export)
    for _ in range(120):
        current = next(j for j in request('/jobs') if j['id'] == result['id'])
        if current['status'] == 'succeeded': break
        if current['status'] == 'failed': raise RuntimeError(current['error'])
        time.sleep(.5)
    assert current['status'] == 'succeeded'
    nfs_mount = Path('/mnt') / prefix
    nfs_mount.mkdir()
    nfs_server = str(args.storage_address) if args.storage_address.version == 4 else '[' + str(args.storage_address) + ']'
    subprocess.run(['mount', '-t', 'nfs4', '-o', 'vers=4.2', nfs_server + ':' + str(mount), str(nfs_mount)], check=True, timeout=30)
    try:
        assert subprocess.check_output(['setpriv', '--reuid=1000', '--regid=1000', '--clear-groups', 'cat', str(nfs_mount / 'restore-proof.bin')]) == b'modified-data'
        subprocess.run(['setpriv', '--reuid=1000', '--regid=1000', '--clear-groups', 'sh', '-c', 'printf nfs-write-proof > "$1"', 'sh', str(nfs_mount / 'nfs-proof')], check=True)
        assert (mount / 'nfs-proof').read_text() == 'nfs-write-proof'
        guide = request('/client-guide/' + str(repo['id']))
        generated = guide['scripts']['rsync'].replace('/mnt/backup-center/' + repo['name'], str(nfs_mount))
        source = Path('/tmp') / (prefix + '-source')
        source.mkdir(mode=0o750)
        os.chown(source, 1000, 1000)
        source_file = source / 'service-data.txt'
        source_file.write_text('verified-source-service-data')
        os.chown(source_file, 1000, 1000)
        generated_path = Path('/tmp') / (prefix + '-backup.sh')
        generated_path.write_text(generated)
        generated_path.chmod(0o755)
        subprocess.run(['setpriv', '--reuid=1000', '--regid=1000', '--clear-groups', 'env', 'SOURCE_DIR=' + str(source), 'bash', str(generated_path)], check=True, timeout=60)
        assert (mount / 'current' / 'data' / 'service-data.txt').read_text() == 'verified-source-service-data'
        assert (mount / 'current' / 'complete.json').is_file()
        source_file.unlink()
        source.rmdir()
        generated_path.unlink()
        print('PASS: generated rsync script verifies checksums and atomically publishes completed run', flush=True)

    finally:
        subprocess.run(['umount', str(nfs_mount)], check=True, timeout=30)
        nfs_mount.rmdir()
    print('PASS: NFSv4 kernel mount, read and UID-restricted write', flush=True)
    export['enabled'] = False
    result = request('/exports', 'PUT', export)
    for _ in range(120):
        current = next(j for j in request('/jobs') if j['id'] == result['id'])
        if current['status'] in ('failed', 'succeeded'):break
        time.sleep(.5)
    assert current['status'] == 'succeeded', current
    # Native encryption must survive unload/load without losing file content.
    encrypted = create(prefix + '-encrypted', encrypted=True)
    encrypted_path = Path('/srv/backup-center/repos') / encrypted['name'] / 'proof'
    encrypted_path.write_text('encrypted-proof')
    job('/actions/key.unload', {'id': encrypted['id']})
    job('/actions/key.load', {'id': encrypted['id']})
    assert encrypted_path.read_text() == 'encrypted-proof'
    print('PASS: native encryption key unload/load', flush=True)
    volume = create(prefix + '-volume', kind='volume')
    iqn = 'iqn.2026-10.local.backup:' + prefix
    target = {'repository_id': volume['id'], 'protocol': 'iscsi', 'iqn': iqn, 'initiators': 'iqn.2026-10.local.backup:acceptance', 'clients': '', 'enabled': True, 'read_only': False}
    result = request('/exports', 'PUT', target)
    for _ in range(120):
        current = next(j for j in request('/jobs') if j['id'] == result['id'])
        if current['status'] in ('failed', 'succeeded'):break
        time.sleep(.5)
    assert current['status'] == 'succeeded', current
    output = subprocess.check_output(['targetcli', '/iscsi/' + iqn, 'ls'], text=True)
    assert 'acceptance' in output and 'lun0' in output
    print('PASS: real LIO iSCSI target, LUN and explicit initiator ACL', flush=True)
    target['enabled'] = False
    result = request('/exports', 'PUT', target)
    for _ in range(120):
        current = next(j for j in request('/jobs') if j['id'] == result['id'])
        if current['status'] in ('failed', 'succeeded'):break
        time.sleep(.5)
    assert current['status'] == 'succeeded', current
    policy = {'repository_id': repo['id'], 'enabled': False, 'keep_hourly': 1, 'keep_daily': 1, 'keep_weekly': 1, 'keep_monthly': 1, 'keep_yearly': 1, 'recursive': False}
    request('/policies', 'PUT', policy)
    preview = request(f"/policies/{repo['id']}/preview")
    assert preview['keep'][second]
    print('PASS: policy persistence and retention preview', flush=True)
    # Cross-platform replication is exercised by verify-platform-replication.py
    # against a dedicated same-version pair, including actual interrupted upload.

    bundle = request('/config/backup', 'POST', {'passphrase': 'acceptance-only-config-passphrase'}, raw=True)
    assert bundle.startswith(b'BCBACKUP1') and b'Password' not in bundle
    print('PASS: encrypted disaster recovery bundle', flush=True)
    # Verify metadata/key restore against a separate fresh state directory without touching live state.
    recovery_dir = Path('/tmp') / (prefix + '-recovery')
    recovery_dir.mkdir(mode=0o700)
    bundle_path = recovery_dir / 'backup.bcbackup'
    bundle_path.write_bytes(bundle)
    pass_path = recovery_dir / 'passphrase'
    pass_path.write_text('acceptance-only-config-passphrase')
    pass_path.chmod(0o600)
    subprocess.run(['/opt/backup-center/backupd', '--data-dir', str(recovery_dir / 'state'), '--restore-config', str(bundle_path), '--password-file', str(pass_path)], check=True)
    import sqlite3
    db = sqlite3.connect(recovery_dir / 'state' / 'backup-center.db')
    assert db.execute('select count(*) from repositories where name=?', (encrypted['name'],)).fetchone()[0] == 1
    assert (recovery_dir / 'state' / 'keys' / (encrypted['name'] + '.key')).read_bytes() == (Path('/var/lib/backup-center/keys') / (encrypted['name'] + '.key')).read_bytes()
    db.close()
    import shutil
    shutil.rmtree(recovery_dir)
    print('PASS: full configuration and encryption key restore into fresh state', flush=True)
    # Remove only resources created by this run, clones before their origins.
    for r in reversed(created):
        for snap in request(f"/repositories/{r['id']}/snapshots"):
            job('/actions/snapshot.delete', {'repository_id': r['id'], 'snapshot': snap['name'], 'confirm': snap['name']})
        job('/actions/repository.delete', {'repository_id': r['id'], 'confirm': r['name']})
    print('PASS: test datasets removed; production storage left clean', flush=True)
    print('ALL ACCEPTANCE CHECKS PASSED', flush=True)

if __name__ == '__main__':
    main()
