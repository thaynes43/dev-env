#!/usr/bin/env python3
"""Hosted-only, pinned source reconstruction. No network retry or compilation."""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys
import tarfile
import urllib.request

HERE = Path(__file__).resolve().parent
LOCK = json.loads((HERE / 'source.lock.json').read_text())
POOL = 'https://deb.debian.org/debian/pool/main/g/git/'


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def download(url, path, cap, expected_sha=None, expected_size=None):
    # Public primary-source URLs only; no token, netrc, shell or retry.
    with urllib.request.urlopen(url, timeout=30) as stream:
        content = stream.read(cap + 1)
    if len(content) > cap:
        raise ValueError('source artifact exceeds cap')
    if expected_size is not None and len(content) != expected_size:
        raise ValueError('source artifact size mismatch')
    if expected_sha is not None and hashlib.sha256(content).hexdigest() != expected_sha:
        raise ValueError('source artifact hash mismatch')
    path.write_bytes(content)
    return content


def extract(path, directory):
    with tarfile.open(path) as archive:
        entries = archive.getmembers()
        if len(entries) > 12000 or sum(e.size for e in entries) > 128 * 1024 * 1024:
            raise ValueError('source archive exceeds extraction cap')
        names = set()
        for entry in entries:
            parts = PurePosixPath(entry.name)
            if parts.is_absolute() or '..' in parts.parts or not (
                    entry.isfile() or entry.isdir()) or parts in names:
                raise ValueError('unsafe source archive entry')
            names.add(parts)
        # Python in the exact base predates tarfile's extraction-filter API.
        # Do not fall back to extractall: allow only validated regular files/dirs,
        # never links/devices/owners/set-id bits, and preserve executable bits.
        for entry in entries:
            path = directory / entry.name
            if entry.isdir():
                path.mkdir(parents=True, exist_ok=True)
            else:
                path.parent.mkdir(parents=True, exist_ok=True)
                with archive.extractfile(entry) as source, path.open('xb') as target:
                    shutil.copyfileobj(source, target, length=16384)
                os.chmod(path, entry.mode & 0o777)


def checksums(dsc):
    lines = dsc.decode('utf-8').splitlines()
    start = lines.index('Checksums-Sha256:') + 1
    result = {}
    for line in lines[start:]:
        if not line.startswith(' '):
            break
        digest, length, name = line.split()
        if len(digest) != 64 or Path(name).name != name or name in result:
            raise ValueError('invalid source checksum declaration')
        result[name] = (digest, int(length))
    return result


def main():
    target = Path(sys.argv[1]).resolve()
    target.mkdir(parents=True)
    artifacts = target / 'artifacts'
    artifacts.mkdir()
    # Resolve the published tag separately from installed package metadata.
    ref = json.loads(download('https://api.github.com/repos/git/git/git/ref/tags/' +
                              LOCK['upstreamTag'], artifacts / 'tag-ref.json', 16384))
    if ref['object']['sha'] != LOCK['upstreamTagObject'] or ref['object']['type'] != 'tag':
        raise ValueError('upstream tag ref mismatch')
    tag = json.loads(download('https://api.github.com/repos/git/git/git/tags/' +
                              LOCK['upstreamTagObject'], artifacts / 'tag.json', 32768))
    if tag['object']['sha'] != LOCK['upstreamCommit'] or tag['object']['type'] != 'commit' or \
            tag['tag'] != LOCK['upstreamTag'] or not tag['verification']['verified']:
        raise ValueError('upstream tag resolution or published verification mismatch')
    upstream = LOCK['upstreamArchive']
    upstream_path = artifacts / 'upstream.tar.gz'
    download(upstream['url'], upstream_path, 16 * 1024 * 1024,
             upstream['sha256'], upstream['bytes'])
    dsc = LOCK['debianDSC']
    dsc_path = artifacts / dsc['file']
    data = download(POOL + dsc['file'], dsc_path, 16384, dsc['sha256'], dsc['bytes'])
    if 'Version: ' + LOCK['debianVersion'] not in data.decode().splitlines():
        raise ValueError('Debian version mismatch')
    files = checksums(data)
    overlay = LOCK['debianOverlay']
    if files.get(overlay['file']) != (overlay['sha256'], overlay['bytes']):
        raise ValueError('Debian overlay disagrees with pinned DSC')
    orig_names = [n for n in files if n.startswith('git_2.39.5.orig.tar.')]
    if len(orig_names) != 1 or set(files) != {orig_names[0], overlay['file']}:
        raise ValueError('unexpected Debian source artifact set')
    for name, (digest, size) in files.items():
        download(POOL + name, artifacts / name, 16 * 1024 * 1024, digest, size)
    up_dir, orig_dir = target / 'upstream', target / 'orig'
    up_dir.mkdir()
    orig_dir.mkdir()
    extract(upstream_path, up_dir)
    extract(artifacts / orig_names[0], orig_dir)
    up_root, = up_dir.iterdir()
    orig_root, = orig_dir.iterdir()
    # Compare every common regular upstream file and require the actual callsites.
    # Release-only generated/omitted files are recorded, not binary equivalence.
    common = []
    omitted = []
    for path in sorted(up_root.rglob('*')):
        if not path.is_file() or path.is_symlink():
            continue
        name = path.relative_to(up_root).as_posix()
        peer = orig_root / name
        if not peer.is_file() or peer.is_symlink():
            omitted.append(name)
        elif path.read_bytes() != peer.read_bytes():
            raise ValueError('upstream source differs from Debian orig: ' + name)
        else:
            common.append(name)
    if not {'read-cache.c', 'object-file.c', 'Makefile', 'config.mak.uname'} <= set(common):
        raise ValueError('missing upstream callsite identity')
    # dpkg-source applies the complete pinned quilt series, including all u3 fixes.
    control = target / 'control'
    subprocess.run(['dpkg-source', '-x', str(dsc_path), str(control)], check=True,
                   timeout=60)
    series = (control / 'debian/patches/series').read_text()
    patch_names = [line.split()[0] for line in series.splitlines()
                   if line.strip() and not line.lstrip().startswith('#')]
    applied = (control / '.pc/applied-patches').read_text().splitlines()
    if len(patch_names) != 7 or patch_names != applied:
        raise ValueError('full Debian u3 patch series not applied')
    # These three files must not be modified by the published Debian quilt patches.
    for name in ['read-cache.c', 'object-file.c', 'Makefile']:
        if (control / name).read_bytes() != (orig_root / name).read_bytes():
            raise ValueError('Debian patch changed reviewed marker callsite')
    instrumented = target / 'instrumented'
    shutil.copytree(control, instrumented)
    patch = HERE / 'git-marker.patch'
    subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(patch)],
                   cwd=instrumented, check=True, timeout=15)
    for name in ['marker.c', 'marker.h']:
        shutil.copy2(HERE / name, instrumented / name)
    # Complete corresponding source is included in the fixture artifact. Copy
    # before compilation, so this never packages object files or build logs.
    package = target / 'source-package'
    package.mkdir()
    shutil.copytree(control, package / 'git')
    shutil.copytree(HERE, package / 'recipe')
    record = dict(abiVersion=1, baseImage=LOCK['baseImage'], upstreamCommit=LOCK['upstreamCommit'],
                  upstreamTagObject=LOCK['upstreamTagObject'],
                  upstreamArchiveSHA256=sha(upstream_path),
                  debianVersion=LOCK['debianVersion'], debianDSCSHA256=sha(dsc_path),
                  debianOrigSHA256=sha(artifacts / orig_names[0]),
                  debianOverlaySHA256=sha(artifacts / overlay['file']),
                  debianSeriesSHA256=hashlib.sha256(series.encode()).hexdigest(),
                  debianPatchCount=len(applied), commonUpstreamFiles=len(common),
                  omittedUpstreamFiles=len(omitted), markerPatchSHA256=sha(patch),
                  markerSourceSHA256=sha(HERE / 'marker.c'),
                  markerHeaderSHA256=sha(HERE / 'marker.h'),
                  artifactVerification='hostedArtifactVerified',
                  historicalCompilerEquivalence=False)
    (target / 'source-provenance.json').write_text(json.dumps(record, sort_keys=True) + '\n')
    (target / 'release-omissions.json').write_text(json.dumps(omitted) + '\n')


if __name__ == '__main__':
    main()
