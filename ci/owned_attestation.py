#!/usr/bin/env python3
"""Bind the media-go source tests and the binaries of the image built from this
exact commit into one attestation for the homelab updater (owned family media-go).

The updater rebuilds the image on the host from the same commit, recipe and
SOURCE_REVISION; the Go build is static, -trimpath and -buildvcs=false with a
digest-pinned SDK, so its binaries must hash exactly as attested here. Stdlib only.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import xml.etree.ElementTree as ET

REPOSITORY = 'monxas/plundrio'
FAMILY = 'media-go'
REQUIRED_CHECK = 'owned-media-go'
WORKFLOW = '.github/workflows/owned-media-go.yml'
# Image path -> bytes the updater reads from the built candidate.
PUBLIC_FILES = ('app/plundrio', 'usr/local/bin/media-validate')
TESTED = 'n1_admission_media_validation_and_rpc_completion_gates_then_runtime_image_binaries'


def require(ok, reason):
    if not ok:
        raise SystemExit('owned attestation refused: ' + reason)


def junit(go_json, output):
    """`go test -json` events -> one JUnit testsuite per package (tests and subtests)."""
    results = {}
    for line in Path(go_json).read_text().splitlines():
        if not line.startswith('{'):
            continue
        event = json.loads(line)
        if event.get('Test') and event.get('Action') in ('pass', 'fail', 'skip'):
            results[(event['Package'], event['Test'])] = (event['Action'], event.get('Elapsed', 0))
    require(bool(results), 'no Go test results')
    root = ET.Element('testsuites')
    total = {'tests': 0, 'failures': 0, 'errors': 0, 'skipped': 0}
    for package in sorted({p for p, _ in results}):
        cases = sorted((t, r) for (p, t), r in results.items() if p == package)
        counts = {'tests': len(cases), 'failures': sum(r[0] == 'fail' for _, r in cases),
                  'errors': 0, 'skipped': sum(r[0] == 'skip' for _, r in cases)}
        suite = ET.SubElement(root, 'testsuite', name=package, **{k: str(v) for k, v in counts.items()})
        for name, (action, elapsed) in cases:
            case = ET.SubElement(suite, 'testcase', classname=package, name=name, time=str(elapsed))
            if action != 'pass':
                ET.SubElement(case, 'failure' if action == 'fail' else 'skipped')
        for key in total:
            total[key] += counts[key]
    raw = ET.tostring(root, encoding='utf-8', xml_declaration=True)
    Path(output).write_bytes(raw)
    require(total['tests'] > 0 and total['failures'] == total['errors'] == total['skipped'] == 0,
            'every Go test must pass; a skipped test is not acceptance')
    return total, {'name': Path(output).name, 'sha256': hashlib.sha256(raw).hexdigest()}


def image_files(image):
    """Exact bytes at each public path of the built image (no container start)."""
    container = subprocess.check_output(['docker', 'create', image]).decode().strip()
    try:
        out = {}
        for path in PUBLIC_FILES:
            stream = subprocess.check_output(['docker', 'cp', container + ':/' + path, '-'])
            with tarfile.open(fileobj=io.BytesIO(stream)) as archive:
                members = [m for m in archive.getmembers() if m.isfile()]
                require(len(members) == 1, 'public path is not one regular file: ' + path)
                raw = archive.extractfile(members[0]).read()
            out[path] = {'sha256': hashlib.sha256(raw).hexdigest(), 'bytes': len(raw)}
        return out
    finally:
        subprocess.run(['docker', 'rm', container], check=True, capture_output=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--go-json', required=True)
    parser.add_argument('--junit', required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]

    def git(*a):
        return subprocess.check_output(['git', '-C', str(root), *a]).decode().strip()

    sha = git('rev-parse', 'HEAD')
    require(re.fullmatch('[0-9a-f]{40}', sha), 'source commit missing')
    require(os.environ.get('GITHUB_SHA') == sha and os.environ.get('GITHUB_REPOSITORY') == REPOSITORY,
            'GitHub run does not attest this exact repository/commit')
    require(os.environ.get('GITHUB_JOB') == FAMILY, 'attestation emitted by the wrong job')
    require(not git('status', '--porcelain', '--untracked-files=no'), 'tracked source changed during the job')
    counts, report = junit(args.go_json, args.junit)
    value = {
        'schema': 1, 'family': FAMILY, 'required_check': REQUIRED_CHECK, 'source_repository': REPOSITORY,
        'source_sha': sha, 'context_relative': '.', 'context_tree': git('rev-parse', sha + '^{tree}'),
        'ci_harness_tree': git('rev-parse', sha + ':ci'),
        'workflow_sha256': hashlib.sha256((root / WORKFLOW).read_bytes()).hexdigest(),
        'run_id': os.environ.get('GITHUB_RUN_ID'), 'run_attempt': os.environ.get('GITHUB_RUN_ATTEMPT'),
        'workflow_job_key': os.environ.get('GITHUB_JOB'),
        'junit': counts, 'test_reports': [report], 'source_tests_passed': True,
        'native_runtime_update_or_product_acceptance': False, 'tested': TESTED,
        'image_built': True, 'build_args': {'SOURCE_REVISION': sha},
        'generated_public_files': image_files(args.image),
        'python': sys.version.split()[0],
        'coverage_limit': 'Fixture providers, generated media and temporary state only; production queue, '
                          'downloads and N1 activity contracts still gate deployment.',
    }
    path = Path(args.output)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')
    print(json.dumps({'required_check': REQUIRED_CHECK, 'source_sha': sha, 'tests': counts['tests'],
                      'generated_public_files': value['generated_public_files'],
                      'attestation_sha256': hashlib.sha256(path.read_bytes()).hexdigest()}))


if __name__ == '__main__':
    main()
