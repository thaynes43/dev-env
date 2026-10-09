#!/usr/bin/env python3
"""Hosted-only low-parallel Git/ABI builds, using the pinned Debian rules' OPTS."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile

HERE = Path(__file__).resolve().parent
EXECUTABLE_BYTE_CAP = 8 * 1024 * 1024
READER_BYTE_CAP = 256 * 1024


def run(argv, *, cwd=None, timeout=600, env=None):
    return subprocess.run(['nice', '-n', '19', *argv], cwd=cwd, check=True,
                          text=True, capture_output=True, timeout=timeout, env=env)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    root, output = map(lambda s: Path(s).resolve(), sys.argv[1:])
    commit = os.environ.get('COMMIT', '')
    if not re.fullmatch('[0-9a-f]{40}', commit):
        raise ValueError('exact recipe source commit required')
    output.mkdir()
    env = dict(os.environ, DEB_BUILD_OPTIONS='parallel=2 nocheck nodoc', LC_ALL='C')
    control, instrumented = root / 'control', root / 'instrumented'
    options = run(['make', '-s', '-f', 'debian/rules', '-f', str(HERE / 'print-options.mk'),
                   'dev-env-marker-options'], cwd=control, timeout=15, env=env).stdout.splitlines()
    keys = [line.partition('=')[0] for line in options]
    required = {'NO_OPENSSL', 'prefix', 'gitexecdir', 'CFLAGS', 'LDFLAGS', 'CC', 'HOST_CPU',
                'USE_LIBPCRE2', 'NO_CROSS_DIRECTORY_HARDLINKS', 'NO_INSTALL_HARDLINKS'}
    if not required <= set(keys) or len(keys) != len(set(keys)) or \
            any('=' not in line or '\x00' in line for line in options):
        raise ValueError('Debian build options unavailable or unexpected')
    values = dict(line.split('=', 1) for line in options)
    if values['CC'] != 'gcc' or values['HOST_CPU'] != 'x86_64' or \
            values['prefix'] != '/usr' or values['gitexecdir'] != '/usr/lib/git-core' or \
            values['NO_OPENSSL'] != '1' or values['USE_LIBPCRE2'] != '1':
        raise ValueError('unsupported Debian compile options')
    # A single binary target avoids docs, upstream wide tests and helper builds.
    for source, destination in [(control, 'git-control'), (instrumented, 'git-wait')]:
        built = run(['make', '-j2', 'git', *options], cwd=source, env=env)
        (root / (destination + '-build.log')).write_text(built.stdout + built.stderr)
        # Preserve Debian compile options, including -g. Apply the same declared
        # post-link debug/symbol stripping to both binaries before hashing them.
        run(['strip', '--strip-unneeded', '-o', str(output / destination),
             str(source / 'git')], timeout=15, env=env)
        if not 0 < (output / destination).stat().st_size <= EXECUTABLE_BYTE_CAP:
            raise ValueError('stripped fixture executable exceeds helper image-file cap')
        (output / destination).chmod(0o755)
    # Same compiler and ABI source as the instrumented marker, not an interposer.
    run(['gcc', '-std=c11', '-O2', '-Wall', '-Wextra', '-Werror', '-fPIC', '-shared',
         '-DGWM_READER_ONLY', '-o', str(output / 'libgit-wait-reader.so'),
         str(HERE / 'marker.c'), '-pthread'], timeout=30)
    if not 0 < (output / 'libgit-wait-reader.so').stat().st_size <= READER_BYTE_CAP:
        raise ValueError('native reader exceeds helper image-file cap')
    run(['gcc', '-std=c11', '-O2', '-Wall', '-Wextra', '-Werror', '-DGWM_TESTING',
         '-o', str(output / 'marker-test'), str(HERE / 'marker.c'),
         str(HERE / 'marker-test.c'), '-pthread'], timeout=30)
    run([str(output / 'marker-test')], timeout=10)
    # Extract the effective upstream platform macros from the actual Make build.
    # GIT-CFLAGS records ALL_CFLAGS. Preprocess via a tiny make target using that
    # same variable, rather than assuming version strings imply stat behavior.
    macro_target = root / 'print-macros.mk'
    macro_target.write_text('dev-env-marker-macros:\n\t@$(CC) $(ALL_CFLAGS) -dM -E '
                            '-include git-compat-util.h - < /dev/null\n')
    active = None
    for source in [control, instrumented]:
        macros = run(['make', '-s', '-f', 'Makefile', '-f', str(macro_target),
                      'dev-env-marker-macros', *options], cwd=source, timeout=15, env=env).stdout
        current = {name: any(line.startswith('#define ' + name + ' ') for line in macros.splitlines())
                   for name in ['USE_STDEV', 'USE_NSEC']}
        if active is not None and current != active:
            raise ValueError('control/instrumented platform stat macros differ')
        active = current
    provenance = json.loads((root / 'source-provenance.json').read_text())
    provenance.update(sourceCommit=commit,
                      buildOptionsSHA256=hashlib.sha256('\n'.join(options).encode()).hexdigest(),
                      compiler=run(['gcc', '-dumpfullversion'], timeout=5).stdout.strip(),
                      postLinkStrip='--strip-unneeded',
                      stripTool=run(['strip', '--version'], timeout=5, env=env).stdout.splitlines()[0],
                      executableByteCap=EXECUTABLE_BYTE_CAP, readerByteCap=READER_BYTE_CAP,
                      platform='linux-amd64', statMacros=active,
                      controlSHA256=digest(output / 'git-control'),
                      executableSHA256=digest(output / 'git-wait'),
                      readerSHA256=digest(output / 'libgit-wait-reader.so'),
                      helperSHA256=digest(Path('/fixture-checks/workspace-cephfs-trial.py')),
                      baseSystemGitSHA256=digest(Path('/usr/bin/git')),
                      baseSystemGitPackage=run(['dpkg-query', '-W', '-f=${Version}', 'git'],
                                               timeout=5).stdout,
                      executable='/opt/dev-env/trials/git-wait',
                      reader='/opt/dev-env/trials/libgit-wait-reader.so')
    if provenance['baseSystemGitPackage'] != provenance['debianVersion']:
        raise ValueError('exact base distro Git differs from reviewed u3')
    serialized = json.dumps(provenance, sort_keys=True) + '\n'
    if len(serialized.encode()) > 4096:
        raise ValueError('provenance exceeds actual helper image-file cap')
    (output / 'git-wait-provenance.json').write_text(serialized)
    # Full options are hashed in the public receipt; keep exact options with source.
    (root / 'build-options.json').write_text(json.dumps(options) + '\n')
    (root / 'source-package/build-options.json').write_text(json.dumps(options) + '\n')
    (root / 'source-package/provenance.json').write_text(json.dumps(provenance, sort_keys=True) + '\n')
    with tarfile.open(output / 'git-wait-source.tar.gz', 'w:gz', compresslevel=1) as archive:
        archive.add(root / 'source-package', arcname='git-wait-source')


if __name__ == '__main__':
    main()
