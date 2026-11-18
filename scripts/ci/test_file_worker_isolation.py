#!/usr/bin/env python3
"""Assert dev-only workload and additive NetworkPolicy contracts on rendered YAML."""
import copy
from pathlib import Path
import shutil
import subprocess
import tempfile
import yaml

COMPONENT = 'app.kubernetes.io/component'
IMAGE = 'ghcr.io/nicrepository/nchat/file-service'


def matches(selector, labels):
    if any(labels.get(k) != v for k, v in selector.get('matchLabels', {}).items()):
        return False
    for expr in selector.get('matchExpressions', []):
        value = labels.get(expr['key'])
        if expr['operator'] == 'In' and value not in expr['values']:
            return False
        if expr['operator'] == 'NotIn' and value in expr['values']:
            return False
    return True


def allowed(docs, labels, direction):
    rules = []
    for d in docs:
        if d['kind'] == 'NetworkPolicy' and matches(d['spec']['podSelector'], labels):
            rules.extend(d['spec'].get(direction, []))
    return rules


def destinations(rules):
    result = set()
    for rule in rules:
        assert rule.get('to') and rule.get('ports'), 'unrestricted worker egress'
        for target in rule['to']:
            assert 'ipBlock' not in target, 'worker internet access'
            labels = target.get('podSelector', {}).get('matchLabels', {})
            comp = labels.get(COMPONENT)
            if 'namespaceSelector' in target:
                assert target['namespaceSelector']['matchLabels'] == {'kubernetes.io/metadata.name': 'kube-system'}
                assert labels == {'k8s-app': 'kube-dns'}
                comp = 'dns'
            assert comp, 'unrestricted peer'
            result.update((comp, p['port'], p.get('protocol', 'TCP')) for p in rule['ports'])
    return result


def workload(objects, mode, api_image):
    name = 'file-service' if mode == 'api' else 'file-' + mode
    d = objects['Deployment', name]
    assert d['spec']['replicas'] == 1
    spec = d['spec']['template']['spec']
    c, = spec['containers']
    env = {e['name']: e for e in c['env']}
    assert env['FILE_BOOT_MODE']['value'] == mode
    assert env['FILE_MALWARE_SCAN_CONCURRENCY']['value'] == '1'
    assert c['image'] == api_image
    assert spec['automountServiceAccountToken'] is False
    assert spec['securityContext']['runAsNonRoot'] is True
    assert spec['securityContext']['seccompProfile']['type'] == 'RuntimeDefault'
    assert c['securityContext'] == {
        'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True,
        'capabilities': {'drop': ['ALL']}}
    assert spec['imagePullSecrets'] == [{'name': 'ghcr-pull'}]
    assert c['envFrom'] == [{'configMapRef': {'name': 'nchat-config'}}]
    for bound in ['requests', 'limits']:
        assert set(c['resources'][bound]) == {'cpu', 'memory', 'ephemeral-storage'}
    probes = [('startupProbe', '/healthz'), ('livenessProbe', '/healthz'),
              ('readinessProbe', '/readyz')]
    for probe, path in probes:
        assert c[probe]['httpGet'] == {'path': path, 'port': 'http'}
    assert all(v.get('emptyDir', {}).get('sizeLimit') for v in spec['volumes'])
    return d, env


def ingress_reachable(docs, labels, destination, port):
    rules = allowed(docs, {COMPONENT: destination, 'app.kubernetes.io/name': 'nchat'}, 'ingress')
    for rule in rules:
        peers = [p['podSelector'] for p in rule.get('from', [])
                 if 'podSelector' in p and 'namespaceSelector' not in p]
        if any(matches(p, labels) for p in peers) and any(p['port'] == port for p in rule.get('ports', [])):
            return True
    return False


def validate_api(docs, labels):
    assert ingress_reachable(docs, labels, 'document-converter', 8089)
    assert not ingress_reachable(docs, labels, 'clamav', 3310)
    converter = []
    for rule in allowed(docs, labels, 'egress'):
        for target in rule.get('to', []):
            comp = target.get('podSelector', {}).get('matchLabels', {}).get(COMPONENT)
            assert comp != 'clamav'
            if comp == 'document-converter':
                converter.append(rule)
    assert destinations(converter) == {('document-converter', 8089, 'TCP')}


def validate_worker_network(docs, d, mode):
    labels = d['spec']['template']['metadata']['labels']
    dependencies = {('postgres', 5432, 'TCP'),
                    ('seaweedfs', 8888, 'TCP'), ('dns', 53, 'UDP'), ('dns', 53, 'TCP')}
    if mode == 'scan-worker':
        dependencies.add(('valkey', 6379, 'TCP'))
    specialized = ('clamav', 3310, 'TCP') if mode == 'scan-worker' else ('document-converter', 8089, 'TCP')
    assert destinations(allowed(docs, labels, 'egress')) == dependencies | {specialized}
    assert allowed(docs, labels, 'ingress') == [{
        'from': [{'podSelector': {'matchLabels': {COMPONENT: 'prometheus'}}}],
        'ports': [{'port': 8083, 'protocol': 'TCP'}]}]
    for dest, port in [('postgres', 5432), ('valkey', 6379), ('seaweedfs', 8888),
                       ('clamav', 3310), ('document-converter', 8089)]:
        expected = (dest != 'clamav' or mode == 'scan-worker') and (dest != 'document-converter' or mode == 'preview-worker')
        if dest == 'valkey':
            expected = mode == 'scan-worker'
        assert ingress_reachable(docs, labels, dest, port) == expected, (mode, dest)
    services = [s['metadata']['name'] for s in docs if s['kind'] == 'Service'
                and s['spec'].get('selector') and matches({'matchLabels': s['spec']['selector']}, labels)]
    assert services == [d['metadata']['name']]


def validate_worker(objects, d, env):
    name = d['metadata']['name']
    spec = d['spec']['template']['spec']
    assert d['spec']['strategy'] == {'type': 'Recreate'}
    assert spec['terminationGracePeriodSeconds'] == 60
    assert spec['serviceAccountName'] == 'nchat-' + name
    assert objects['ServiceAccount', 'nchat-' + name]['automountServiceAccountToken'] is False
    assert not any('JWT' in k or 'CLOUDFLARE' in k or 'MIGRATIONS' in k for k in env)
    assert env['FILE_LINK_SAFETY_ENABLED']['value'] == 'false'
    if name == 'file-preview-worker':
        assert 'VALKEY_URL' not in env
    assert objects['Service', name]['spec']['type'] == 'ClusterIP'


def validate_guardian(objects):
    guardian = objects['Deployment', 'clamav']
    assert guardian['spec']['replicas'] == 0, 'B candidate must remain dormant'
    clamd, = guardian['spec']['template']['spec']['containers']
    assert '--mode=serve' in clamd['args']
    assert clamd['readinessProbe']['httpGet'] == {'path': '/readyz', 'port': 'clamd'}
    assert objects['CronJob', 'clamav-signature-updater']['spec']['suspend'] is True
    config = objects['ConfigMap', 'nchat-config']['data']
    assert config['FILE_MALWARE_SIGNATURE_MAX_AGE_SECONDS'] == '86400'
    assert config['FILE_MALWARE_SCAN_REQUIRED'] == 'true'
    assert config['FILE_MALWARE_SCAN_TIMEOUT_SECONDS'] == '300'


def validate(docs):
    objects = {(d['kind'], d['metadata']['name']): d for d in docs}
    api_image = objects['Deployment', 'file-service']['spec']['template']['spec']['containers'][0]['image']
    assert api_image.startswith(IMAGE + ':') or api_image.startswith(IMAGE + '@')
    for mode in ['api', 'scan-worker', 'preview-worker']:
        d, env = workload(objects, mode, api_image)
        if mode == 'api':
            validate_api(docs, d['spec']['template']['metadata']['labels'])
        else:
            validate_worker(objects, d, env)
            validate_worker_network(docs, d, mode)
    validate_guardian(objects)
    assert not any(d['kind'] in ['HorizontalPodAutoscaler', 'ScaledObject'] and 'file' in d['metadata']['name'] for d in docs)
    for d in docs:
        if d['kind'] in ['RoleBinding', 'ClusterRoleBinding']:
            assert not any(s['name'] in ['nchat-file-scan-worker', 'nchat-file-preview-worker'] for s in d.get('subjects', []))


ROOT = Path(__file__).resolve().parents[2]


def render_fixture(tree):
    """Synthetic render only: no approval, registry publication or cluster access."""
    shutil.copytree(ROOT / 'infra/k8s', tree)
    overlay = tree / 'overlays/nchat-dev-server'
    shutil.copyfile(ROOT / 'scripts/ci/testdata/nchat-dev-topology.env', overlay / 'topology.env')
    default = subprocess.run(['kustomize', 'build', str(overlay)], check=True, capture_output=True, text=True)
    default_docs = list(yaml.safe_load_all(default.stdout))
    assert not any(d['metadata']['name'] in ['file-scan-worker', 'file-preview-worker'] for d in default_docs)
    default_config = next(d for d in default_docs if d['kind'] == 'ConfigMap' and d['metadata']['name'] == 'nchat-config')
    assert 'FILE_MALWARE_SIGNATURE_MAX_AGE_SECONDS' not in default_config['data']
    candidate = tree / 'candidates/clamav-capacity-isolation'
    assert not (candidate / 'kustomization.yaml').exists(), 'candidate activated by default'
    guardian = candidate / 'guardian'
    guardian.mkdir()
    values = {'HELPER_IMAGE': 'example.invalid/fixture@sha256:' + 'b' * 64,
              'STORAGE_CLASS': 'fixture-rwx', 'MAX_AGE': '86400',
              'APPROVAL': 'TEST-ONLY-NOT-APPROVED', 'MIRROR': 'https://fixture.invalid',
              'MIRROR_CIDR': '192.0.2.1/32'}
    for source in (tree / 'candidates/clamav-signatures').glob('*.yaml.in'):
        text = source.read_text()
        for key, value in values.items():
            text = text.replace('__' + key + '__', value)
        (guardian / source.name.removesuffix('.in')).write_text(text)
    template = (candidate / 'kustomization.yaml.in').read_text()
    template = template.replace('__APPROVED_MAX_AGE_SECONDS__', '86400')
    template = template.replace('__APPROVED_FILE_SERVICE_DIGEST__', 'a' * 64)
    (candidate / 'kustomization.yaml').write_text(template)
    result = subprocess.run(['kustomize', 'build', str(candidate)], check=True, capture_output=True, text=True)
    assert not result.stderr, result.stderr
    docs = list(yaml.safe_load_all(result.stdout))
    validate(docs)
    images = [d['spec']['template']['spec']['containers'][0]['image'] for d in docs
              if d['kind'] == 'Deployment' and d['metadata']['name'] in
              ['file-service', 'file-scan-worker', 'file-preview-worker']]
    assert images == [IMAGE + '@sha256:' + 'a' * 64] * 3
    return docs


def reject_unsafe_mutations(docs):
    mutations = [
        ('Deployment', 'file-service', lambda d: d['spec']['template']['spec']['containers'][0]['env'].append({'name': 'FILE_BOOT_MODE', 'value': 'combined'})),
        ('Deployment', 'file-scan-worker', lambda d: d['spec'].update(replicas=2)),
        ('Deployment', 'file-scan-worker', lambda d: d['spec']['template']['spec'].update(automountServiceAccountToken=True)),
        ('Deployment', 'file-preview-worker', lambda d: d['spec']['template']['spec']['containers'][0]['resources'].pop('limits')),
        ('NetworkPolicy', 'nchat-allow-file-scan-worker-egress', lambda d: d['spec']['egress'].append({})),
        ('Deployment', 'clamav', lambda d: d['spec'].update(replicas=1)),
    ]
    for kind, name, mutate in mutations:
        changed = copy.deepcopy(docs)
        mutate(next(d for d in changed if d['kind'] == kind and d['metadata']['name'] == name))
        try:
            validate(changed)
        except (AssertionError, KeyError):
            continue
        raise AssertionError('unsafe mutation accepted: ' + name)


if __name__ == '__main__':
    with tempfile.TemporaryDirectory(prefix='nchat-capacity-fixture-') as directory:
        reject_unsafe_mutations(render_fixture(Path(directory) / 'k8s'))
    print('PASS: dormant E/B candidate; roles, capacity, policies, same digest; six unsafe mutations rejected')
