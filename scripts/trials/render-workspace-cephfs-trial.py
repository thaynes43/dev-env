#!/usr/bin/env python3
"""PRIVATE manifest renderer only; never creates resources."""
import argparse
import hashlib
import json
from pathlib import Path
import re

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output-dir', required=True)
parser.add_argument('--source-commit', default='proposal-unmerged')
parser.add_argument('--git-wait-image', help='exact separately reviewed signed fixture image digest')
args = parser.parse_args()
if args.source_commit != 'proposal-unmerged' and not re.fullmatch('[0-9a-f]{40}',args.source_commit):
    parser.error('--source-commit must be an exact reviewed commit or proposal-unmerged')
if args.git_wait_image:
    match = re.fullmatch(r'ghcr\.io/thaynes43/dev-env:git-wait-sha-([0-9a-f]{40})@sha256:[0-9a-f]{64}',
                         args.git_wait_image)
    if not match or match[1] != args.source_commit:
        parser.error('--git-wait-image must pin the exact fixture digest and matching source commit')
output_dir = Path(args.output_dir)
output_dir.mkdir(parents=True,exist_ok=True)

source_path = Path(__file__).resolve().parent/'workspace-cephfs-trial.py'
source = source_path.read_text()
annotations = {'k8tz.io/inject':'false',
               'dev-env.haynesops.com/trial-source-sha256':hashlib.sha256(source.encode()).hexdigest(),
               'dev-env.haynesops.com/trial-source-commit':args.source_commit}
image = 'ghcr.io/thaynes43/dev-env:2.9.1@sha256:a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf'
init = '''import json, os, pathlib
root = pathlib.Path('/trial-workspace')
for name in ('repos','codex','work'):
    (root/name).mkdir(exist_ok=True)
marker = root/'repos'/'.fixture-identity.json'
temporary = root/'repos'/('.identity-'+os.environ['TRIAL_POD_UID']+'.tmp')
temporary.write_text(json.dumps({'runID':'workspace-cephfs-trial-20261009-v1'}))
try:
    os.link(temporary,marker)
except FileExistsError:
    pass
finally:
    temporary.unlink()
assert json.loads(marker.read_text()) == {'runID':'workspace-cephfs-trial-20261009-v1'}
'''
security = {'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,
            'capabilities':{'drop':['ALL']}}
pod_uid = {'name':'TRIAL_POD_UID','valueFrom':{'fieldRef':{'fieldPath':'metadata.uid'}}}
for role,node in [('a','talosw01'),('b','talosw02'),('reconnect','talosw03')]:
    name = 'dev-env-workspace-cephfs-'+role
    native_marker = bool(args.git_wait_image and role in {'a','b'})
    labels = {'app.kubernetes.io/name':'dev-env-workspace-cephfs-trial',
              'dev-env.haynesops.com/trial-role':role}
    pod = {
        'restartPolicy':'Never','automountServiceAccountToken':False,
        'enableServiceLinks':False,'terminationGracePeriodSeconds':10,
        'priorityClassName':'dev-env-agent','preemptionPolicy':'Never',
        'nodeSelector':{'topology.kubernetes.io/zone':'w','kubernetes.io/hostname':node},
        'affinity':{'nodeAffinity':{'requiredDuringSchedulingIgnoredDuringExecution':{
            'nodeSelectorTerms':[{'matchExpressions':[{'key':'node-role.kubernetes.io/control-plane','operator':'DoesNotExist'}]}]}}},
        'securityContext':{'runAsNonRoot':True,'runAsUser':1000,'runAsGroup':1000,
                           'fsGroup':1000,'fsGroupChangePolicy':'OnRootMismatch',
                           'seccompProfile':{'type':'RuntimeDefault'}},
        'initContainers':[{
            'name':'prepare-empty-trial-directories','image':image,'imagePullPolicy':'IfNotPresent',
            'command':['/usr/local/bin/tini','--','/usr/bin/nice','-n','19','python3','-c'],
            'args':[init],'env':[pod_uid],
            'resources':{'requests':{'cpu':'25m','memory':'32Mi'},
                         'limits':{'cpu':'100m','memory':'128Mi'}},
            'securityContext':security,
            'volumeMounts':[{'name':'workspace','mountPath':'/trial-workspace'},
                            {'name':'tmp','mountPath':'/tmp'}]}],
        'containers':[{
            'name':'trial','image':args.git_wait_image if native_marker else image,'imagePullPolicy':'IfNotPresent',
            'command':['/usr/local/bin/tini','--','/usr/bin/nice','-n','19','python3','-c'],
            'args':[source],
            'env':[{'name':'TRIAL_ROLE','value':role},
                   {'name':'TRIAL_CONTAINER','value':'disposable-cephfs'},pod_uid,
                   {'name':'TRIAL_NODE','valueFrom':{'fieldRef':{'fieldPath':'spec.nodeName'}}}],
            'resources':{'requests':{'cpu':'50m','memory':'128Mi','ephemeral-storage':'64Mi'},
                         'limits':{'cpu':'250m','memory':'256Mi','ephemeral-storage':'256Mi'}},
            'securityContext':security,
            'volumeMounts':[{'name':'home','mountPath':'/home/dev'},
                {'name':'workspace','mountPath':'/home/dev/repos','subPath':'repos'},
                {'name':'workspace','mountPath':'/home/dev/codex','subPath':'codex'},
                {'name':'workspace','mountPath':'/home/dev/work','subPath':'work'},
                {'name':'tmp','mountPath':'/tmp'}]}],
        'volumes':[{'name':'workspace','persistentVolumeClaim':{'claimName':'dev-env-workspace-cephfs-trial'}},
                   {'name':'home','emptyDir':{'sizeLimit':'64Mi'}},
                   {'name':'tmp','emptyDir':{'sizeLimit':'64Mi'}}],
    }
    if native_marker:
        pod['containers'][0]['env'].append({'name':'TRIAL_GIT_WAIT_MARKER','value':'1'})
        pod['containers'][0]['env'].append({'name':'TRIAL_HELPER_SHA256',
            'value':annotations['dev-env.haynesops.com/trial-source-sha256']})
        pod['containers'][0]['env'].append({'name':'TRIAL_HELPER_SOURCE_COMMIT',
            'value':args.source_commit})
    job = {'apiVersion':'batch/v1','kind':'Job',
           'metadata':{'name':name,'namespace':'dev-agents','labels':labels,
                       'annotations':annotations},
           'spec':{'backoffLimit':0,'completions':1,'parallelism':1,
                   'activeDeadlineSeconds':180 if role in {'a','b'} else 60,
                   'ttlSecondsAfterFinished':600,
                   'template':{'metadata':{'labels':labels,'annotations':annotations},'spec':pod}}}
    output = output_dir/('workspace-cephfs-trial-'+role+'.job.json')
    output.write_text(json.dumps(job,indent=2)+'\n')
    output.chmod(0o600)
    print(str(output))
