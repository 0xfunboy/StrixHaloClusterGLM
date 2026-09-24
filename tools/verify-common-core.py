#!/usr/bin/env python3
"""Read-only shared-source integrity check. Never loads models or edits repositories."""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path


def verify(root: Path) -> dict:
    root = root.resolve()
    manifest = json.loads((root / 'common-core.lock.json').read_text())
    if manifest.get('schema') != 'strix-common-core-v1':
        raise ValueError('unsupported common-core schema')
    files = manifest['files']
    if not isinstance(files, dict) or not files:
        raise ValueError('empty shared-source set')
    for relative, expected in files.items():
        p = (root / relative).resolve()
        if not p.is_relative_to(root) or Path(relative).is_absolute():
            raise ValueError('unsafe common-source path: ' + relative)
        if hashlib.sha256(p.read_bytes()).hexdigest() != expected:
            raise ValueError('common-source drift: ' + str(p))
    profile = json.loads((root / 'model-profile.json').read_text())
    if profile.get('schema') != 'strix-model-profile-v1' or not profile.get('model_id'):
        raise ValueError('invalid model profile')
    config_path = (root / profile['gateway_config']).resolve()
    if not config_path.is_relative_to(root):
        raise ValueError('profile config outside repository')
    config = json.loads(config_path.read_text())
    if config.get('model') != profile['serving_model_id']:
        raise ValueError('profile/gateway model identity mismatch')
    if profile.get('frontend_qualified') is False and config.get('inference_enabled') is not False:
        raise ValueError('unqualified frontend profile must refuse inference')
    return {'repository': str(root), 'status': 'PASS', 'common_files': len(files),
            'core_id': manifest['core_id'], 'index_sha256': hashlib.sha256((root / 'common-core.lock.json').read_bytes()).hexdigest(),
            'model_id': profile['model_id'], 'model_inference_calls': 0}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('repositories', nargs='*', type=Path)
    args = parser.parse_args()
    roots = args.repositories or [Path(__file__).resolve().parents[1]]
    results = [verify(root) for root in roots]
    if len({row['index_sha256'] for row in results}) != 1:
        raise ValueError('repositories do not share the same common-core lock')
    if len({row['model_id'] for row in results}) != len(results):
        raise ValueError('duplicate model-family identity')
    print(json.dumps({'status': 'COMMON_CORE_ALIGNMENT_PASS', 'repositories': results}, indent=2))


if __name__ == '__main__':
    main()
